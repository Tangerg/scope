package azurecosmos

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a Cosmos DB SQL
// predicate fragment. Metadata keys live under c.metadata.* by default
// (the document alias used in Search / DeleteWhere is `c`).
//
// Output shape:
//
//	author == "Alice"        →  c.metadata.author = @p1
//	year >= 2020             →  c.metadata.year >= @p1
//	category IN ("a", "b")   →  c.metadata.category IN (@p1, @p2)
//	NOT (a == "x")           →  NOT (c.metadata.a = @p1)
//	a == "x" AND b == "y"    →  (c.metadata.a = @p1 AND c.metadata.b = @p2)
type visitor struct {
	err            error
	sql            strings.Builder
	params         []NamedParam
	alias          string
	metadataPrefix string
}

// NamedParam pairs a `@N`-style placeholder with its value. Cosmos
// SDK uses named parameters via QueryParameters.
type NamedParam struct {
	Name  string
	Value any
}

func newVisitor(alias, metadataPrefix string) *visitor {
	if alias == "" {
		alias = "c"
	}
	return &visitor{alias: alias, metadataPrefix: metadataPrefix}
}

func (v *visitor) snapshot() (string, []NamedParam) {
	if v.err != nil {
		return "", nil
	}
	return v.sql.String(), v.params
}

func (v *visitor) Visit(expr filter.Predicate) error {
	v.sql.Reset()
	v.params = nil
	v.err = v.visit(expr)
	return v.err
}

func (v *visitor) visit(expr filter.Expr) error {
	switch node := expr.(type) {
	case *filter.BinaryExpr:
		return node.Dispatch(filter.BinaryHandlers{
			Logical:    v.visitLogicalExpr,
			Comparison: v.visitComparisonExpr,
			In:         v.visitInExpr,
			Has:        v.visitHasExpr,
			Like:       v.visitLikeExpr,
			NullTest:   v.visitNullTestExpr,
		})
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	default:
		return fmt.Errorf("azurecosmos: unsupported root expression %T", node)
	}
}

// visitNullTestExpr emits `(NOT IS_DEFINED(<path>) OR IS_NULL(<path>))`.
//
// IS_NULL alone is not enough: the documented example evaluates
// IS_NULL({quantity: 25, vendor: null}.size) to false, so an absent property
// is not null to Cosmos — while it is nil to the filter AST, and absent is the
// ordinary case for metadata. IS_DEFINED separates the two states, so the
// disjunction covers exactly the ones the AST reads as nil. The negated IS NOT
// NULL arrives as NOT(...) and is rendered by visitUnaryExpr.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	v.sql.WriteString("(NOT IS_DEFINED(")
	v.sql.WriteString(field)
	v.sql.WriteString(") OR IS_NULL(")
	v.sql.WriteString(field)
	v.sql.WriteString("))")
	return nil
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	value, err := expr.Value()
	if err != nil {
		return err
	}
	v.sql.WriteString("ARRAY_CONTAINS(")
	v.sql.WriteString(field)
	v.sql.WriteString(", ")
	v.sql.WriteString(v.bindParam(value))
	v.sql.WriteByte(')')
	return nil
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if !expr.Operator().Is(filter.OpNot) {
		return fmt.Errorf("azurecosmos: unsupported unary '%s'", expr.Operator().String())
	}
	v.sql.WriteString("NOT (")
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	op := " AND "
	if expr.Operator().Is(filter.OpOr) {
		op = " OR "
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
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	value, err := expr.Value()
	if err != nil {
		return err
	}
	op, err := sqlOpFor(expr.Operator())
	if err != nil {
		return err
	}
	param := v.bindParam(value)
	v.sql.WriteString(field)
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	v.sql.WriteString(param)
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return errors.New("azurecosmos: 'IN' requires a list on the right")
	}
	if listLit.Len() == 0 {
		return errors.New("azurecosmos: 'IN' requires a non-empty list")
	}

	v.sql.WriteString(field)
	v.sql.WriteString(" IN (")
	for i, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return err
		}
		if i > 0 {
			v.sql.WriteString(", ")
		}
		v.sql.WriteString(v.bindParam(val))
	}
	v.sql.WriteByte(')')
	return nil
}

// visitLikeExpr maps exactly representable SQL LIKE shapes onto Cosmos string
// functions. Patterns with internal or single-character wildcards are rejected
// instead of being approximated.
func (v *visitor) visitLikeExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	value, err := expr.Value()
	if err != nil {
		return err
	}
	pattern, ok := value.(string)
	if !ok {
		return fmt.Errorf("azurecosmos: LIKE requires a string pattern, got %T", value)
	}
	if strings.ContainsRune(pattern, '_') {
		return errors.New("azurecosmos: LIKE '_' wildcard is not supported by Cosmos string functions")
	}
	leadingWildcard := strings.HasPrefix(pattern, "%")
	trailingWildcard := strings.HasSuffix(pattern, "%")
	text := strings.TrimSuffix(strings.TrimPrefix(pattern, "%"), "%")
	if text == "" || strings.ContainsRune(text, '%') {
		return fmt.Errorf("azurecosmos: LIKE pattern %q cannot be represented exactly", pattern)
	}
	param := v.bindParam(text)
	switch {
	case leadingWildcard && trailingWildcard:
		v.sql.WriteString("CONTAINS(")
		v.sql.WriteString(field)
		v.sql.WriteString(", ")
		v.sql.WriteString(param)
		v.sql.WriteByte(')')
	case leadingWildcard:
		v.sql.WriteString("ENDSWITH(")
		v.sql.WriteString(field)
		v.sql.WriteString(", ")
		v.sql.WriteString(param)
		v.sql.WriteByte(')')
	case trailingWildcard:
		v.sql.WriteString("STARTSWITH(")
		v.sql.WriteString(field)
		v.sql.WriteString(", ")
		v.sql.WriteString(param)
		v.sql.WriteByte(')')
	default:
		v.sql.WriteString(field)
		v.sql.WriteString(" = ")
		v.sql.WriteString(param)
	}
	return nil
}

func (v *visitor) fieldPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.Path()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("empty key path")
	}
	parts := []string{v.alias}
	if v.metadataPrefix != "" {
		parts = append(parts, v.metadataPrefix)
	}
	parts = append(parts, keys...)
	return strings.Join(parts, "."), nil
}

func sqlOpFor(kind filter.Operator) (string, error) {
	switch kind {
	case filter.OpEqual:
		return "=", nil
	case filter.OpNotEqual:
		return "<>", nil
	case filter.OpLess:
		return "<", nil
	case filter.OpLessEqual:
		return "<=", nil
	case filter.OpGreater:
		return ">", nil
	case filter.OpGreaterEqual:
		return ">=", nil
	default:
		return "", fmt.Errorf("azurecosmos: unexpected operator '%s'", kind.Name())
	}
}

func (v *visitor) bindParam(value any) string {
	name := fmt.Sprintf("@p%d", len(v.params)+1)
	v.params = append(v.params, NamedParam{Name: name, Value: value})
	return name
}
