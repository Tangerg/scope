package vectara

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into Vectara's
// metadata-filter syntax. Vectara addresses document-level metadata
// under the `doc.` prefix; the visitor honors a caller-supplied
// override so part-level metadata (`part.`) can be filtered too.
//
// Output shape (default prefix "doc."):
//
//	author == "Alice"        →  doc.author = 'Alice'
//	year >= 2020             →  doc.year >= 2020
//	tag IN ("a", "b")        →  doc.tag IN ('a', 'b')
//	NOT (year >= 2020)       →  NOT (doc.year >= 2020)
type visitor struct {
	err            error
	sql            strings.Builder
	metadataPrefix string
}

func newVisitor(metadataPrefix string) *visitor {
	if metadataPrefix == "" {
		metadataPrefix = "doc"
	}
	return &visitor{metadataPrefix: metadataPrefix}
}

func (v *visitor) snapshot() string {
	if v.err != nil {
		return ""
	}
	return v.sql.String()
}

func (v *visitor) Visit(expr filter.Predicate) error {
	v.sql.Reset()
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
			Has: func(expr *filter.BinaryExpr) error {
				return fmt.Errorf("vectara: HAS is not supported because Vectara filterable metadata fields are scalar at %s",
					expr.Start().String())
			},
			Like:     v.visitLikeExpr,
			NullTest: v.visitNullTestExpr,
		})
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	default:
		return fmt.Errorf("vectara: unsupported root expression %T", node)
	}
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if !expr.Operator().Is(filter.OpNot) {
		return fmt.Errorf("vectara: unsupported unary '%s'", expr.Operator().String())
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

// visitNullTestExpr emits `<path> IS NULL`. Vectara documents its null
// operators as checking "whether or not a value is NULL (empty or missing)",
// which is the same pair of states the filter AST reads as nil. The negated
// IS NOT NULL arrives as NOT(...) and is rendered by visitUnaryExpr.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	v.sql.WriteString("(")
	v.sql.WriteString(field)
	v.sql.WriteString(" IS NULL)")
	return nil
}

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	lit, err := expr.Literal()
	if err != nil {
		return err
	}
	term, err := literalToSQL(lit)
	if err != nil {
		return err
	}
	op, err := opFor(expr.Operator())
	if err != nil {
		return err
	}
	v.sql.WriteString(field)
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	v.sql.WriteString(term)
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return errors.New("vectara: 'IN' requires a list on the right")
	}
	if listLit.Len() == 0 {
		return errors.New("vectara: 'IN' requires a non-empty list")
	}
	parts := make([]string, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		term, err := literalToSQL(lit)
		if err != nil {
			return err
		}
		parts = append(parts, term)
	}
	v.sql.WriteString(field)
	v.sql.WriteString(" IN (")
	v.sql.WriteString(strings.Join(parts, ", "))
	v.sql.WriteByte(')')
	return nil
}

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
		return fmt.Errorf("vectara: LIKE requires a string pattern, got %T", value)
	}
	v.sql.WriteString(field)
	v.sql.WriteString(" LIKE ")
	v.sql.WriteString(quoteSQLString(pattern))
	return nil
}

func (v *visitor) fieldPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.IdentifierPath()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("empty key path")
	}
	return v.metadataPrefix + "." + strings.Join(keys, "."), nil
}

func opFor(kind filter.Operator) (string, error) {
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
		return "", fmt.Errorf("vectara: unexpected operator '%s'", kind.Name())
	}
}

// literalToSQL renders a filter literal as a Vectara metadata-filter term.
//
// It reads the literal rather than a scalar decoded out of it because the
// literal owns the exact numeral and a scalar cannot carry it back. Deciding
// integer-ness with float64(int64(value)) == value asked Go for an
// out-of-range float-to-int conversion, which the spec leaves
// implementation-defined: at 2^63 arm64 saturates to MaxInt64, whose float64
// compares equal, so the term became 9223372036854775807 while amd64 emitted
// the right digits. An integer past int64 had no branch at all and fell
// through to a %v rendering nobody owned.
func literalToSQL(lit *filter.Literal) (string, error) {
	switch {
	case lit.IsString():
		text, err := lit.AsString()
		if err != nil {
			return "", fmt.Errorf("vectara: %w (at %s)", err, lit.Start().String())
		}
		return quoteSQLString(text), nil
	case lit.IsNumber():
		text, err := lit.NumberText()
		if err != nil {
			return "", fmt.Errorf("vectara: %w (at %s)", err, lit.Start().String())
		}
		return text, nil
	case lit.IsBool():
		value, err := lit.AsBool()
		if err != nil {
			return "", fmt.Errorf("vectara: %w (at %s)", err, lit.Start().String())
		}
		return strconv.FormatBool(value), nil
	default:
		return "", fmt.Errorf("vectara: unsupported literal kind '%s' at %s",
			lit.Kind(), lit.Start().String())
	}
}

func quoteSQLString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
