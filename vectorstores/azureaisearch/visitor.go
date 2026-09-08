package azureaisearch

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into Azure AI Search OData
// `$filter` syntax. Metadata is treated as flat top-level fields on
// the indexed document — Azure AI Search doesn't support nested
// metadata in $filter expressions, so each filterable key must exist
// as its own top-level field on the index schema.
//
// Output shape:
//
//	author == "Alice"          →  author eq 'Alice'
//	year >= 2020               →  year ge 2020
//	category IN ("a", "b")     →  search.in(category, 'a,b', ',')
//	NOT (year >= 2020)         →  not (year ge 2020)
type visitor struct {
	err error
	sql strings.Builder
}

func newVisitor() *visitor { return &visitor{} }

func (v *visitor) snapshot() string {
	if v.err != nil {
		return ""
	}
	return v.sql.String()
}

func (v *visitor) Visit(expr filter.Predicate) error {
	v.err = nil
	v.sql.Reset()
	v.err = v.visit(expr)
	return v.err
}

func (v *visitor) visit(expr filter.Expr) error {
	if expr == nil {
		return errors.New("azureaisearch: cannot process nil expression")
	}
	if v.err != nil {
		return v.err
	}
	switch node := expr.(type) {
	case *filter.BinaryExpr:
		return v.visitBinaryExpr(node)
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	default:
		return fmt.Errorf("azureaisearch: unsupported root expression %T", node)
	}
}

func (v *visitor) visitBinaryExpr(expr *filter.BinaryExpr) error {
	switch {
	case expr.Operator().IsLogicalOperator():
		return v.visitLogicalExpr(expr)
	case expr.Operator().Is(filter.OpIn):
		return v.visitInExpr(expr)
	case expr.Operator().Is(filter.OpHas):
		return v.visitHasExpr(expr)
	case expr.Operator().Is(filter.OpLike):
		return v.visitLikeExpr(expr)
	case expr.Operator().IsNullOperator():
		return v.visitNullTestExpr(expr)
	case expr.Operator().IsEqualityOperator() || expr.Operator().IsOrderingOperator():
		return v.visitComparisonExpr(expr)
	default:
		return fmt.Errorf("azureaisearch: unsupported binary operator '%s'", expr.Operator().String())
	}
}

// visitNullTestExpr emits `<field> eq null`, which OData documents as
// matching a field that "will be null if it was never set, or if it was
// explicitly set to null" — the same two states the filter AST reads as nil.
// The negated IS NOT NULL arrives as NOT(...) and is rendered by
// visitUnaryExpr, so no separate handling is needed here.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	field, err := fieldName(expr.Left())
	if err != nil {
		return err
	}
	v.sql.WriteString(field)
	v.sql.WriteString(" eq null")
	return nil
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	field, err := fieldName(expr.Left())
	if err != nil {
		return err
	}
	value, err := expr.Value()
	if err != nil {
		return err
	}
	v.sql.WriteString(field)
	v.sql.WriteString("/any(element: element eq ")
	v.sql.WriteString(odataLiteral(value))
	v.sql.WriteByte(')')
	return nil
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if !expr.Operator().Is(filter.OpNot) {
		return fmt.Errorf("azureaisearch: unsupported unary '%s'", expr.Operator().String())
	}
	v.sql.WriteString("not (")
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	op := " and "
	if expr.Operator().Is(filter.OpOr) {
		op = " or "
	}
	v.sql.WriteString("(")
	if err := v.visit(expr.Left()); err != nil {
		return err
	}
	v.sql.WriteString(op)
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	field, err := fieldName(expr.Left())
	if err != nil {
		return err
	}
	value, err := expr.Value()
	if err != nil {
		return err
	}
	op, err := odataOpFor(expr.Operator())
	if err != nil {
		return err
	}
	v.sql.WriteString(field)
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	v.sql.WriteString(odataLiteral(value))
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	field, err := fieldName(expr.Left())
	if err != nil {
		return err
	}
	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return errors.New("azureaisearch: 'IN' requires a list on the right")
	}
	if listLit.Len() == 0 {
		return errors.New("azureaisearch: 'IN' requires a non-empty list")
	}

	parts := make([]string, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return err
		}
		s := fmt.Sprint(val)
		// search.in's third argument is the separator — pick something
		// that's unlikely to appear in tag values.
		parts = append(parts, strings.ReplaceAll(s, "|", `\|`))
	}
	v.sql.WriteString("search.in(")
	v.sql.WriteString(field)
	v.sql.WriteString(", '")
	v.sql.WriteString(strings.ReplaceAll(strings.Join(parts, "|"), "'", "''"))
	v.sql.WriteString("', '|')")
	return nil
}

// visitLikeExpr maps LIKE onto Azure AI Search's wildcard syntax via
// search.ismatch. The full Lucene wildcard syntax `*` / `?` is what
// AI Search expects; SQL's `%` / `_` are forwarded accordingly.
func (v *visitor) visitLikeExpr(expr *filter.BinaryExpr) error {
	// Azure's $filter has no string function to build a pattern match on: the
	// only Boolean functions are geo.intersects, search.in, search.ismatch and
	// search.ismatchscoring. The last two run a full-text query, which answers
	// a different question — the field is analyzed, so search.ismatch('Alice')
	// matches a document whose author is "Alice Smith" or "alice", and Azure's
	// own example notes that a search for "waterfront" also matches "water"
	// and "front". Substituting it would make one filter mean tokenized,
	// case-insensitive, substring matching here and whole-value,
	// case-sensitive matching everywhere else.
	return fmt.Errorf("azureaisearch: LIKE operator is not supported in Azure AI Search $filter at %s",
		expr.Start().String())
}

// fieldName extracts the (flat) field identifier — Azure AI Search
// doesn't support nested-property paths in $filter, so the left
// operand must reduce to a single bare identifier.
func fieldName(expr filter.Expr) (string, error) {
	switch node := expr.(type) {
	case *filter.Ident:
		return node.Name(), nil
	case *filter.IndexExpr:
		keys, err := node.Path()
		if err != nil {
			return "", err
		}
		if len(keys) != 1 {
			return "", fmt.Errorf("azureaisearch: nested paths are not supported; got %s",
				strings.Join(keys, "."))
		}
		return keys[0], nil
	default:
		return "", fmt.Errorf("unsupported left operand %T", node)
	}
}

func odataOpFor(kind filter.Operator) (string, error) {
	switch kind {
	case filter.OpEqual:
		return "eq", nil
	case filter.OpNotEqual:
		return "ne", nil
	case filter.OpLess:
		return "lt", nil
	case filter.OpLessEqual:
		return "le", nil
	case filter.OpGreater:
		return "gt", nil
	case filter.OpGreaterEqual:
		return "ge", nil
	default:
		return "", fmt.Errorf("azureaisearch: unexpected operator '%s'", kind.Name())
	}
}

func odataLiteral(v any) string {
	switch val := v.(type) {
	case string:
		return "'" + strings.ReplaceAll(val, "'", "''") + "'"
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		if float64(int64(val)) == val {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	default:
		return fmt.Sprint(val)
	}
}
