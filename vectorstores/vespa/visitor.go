package vespa

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a Vespa YQL `where`
// clause. The metadata fields must be declared in the Vespa schema
// (sd file) — Vespa addresses them as flat top-level attributes.
//
// Output shape (when `metadataPrefix` is empty):
//
//	author == "Alice"        →  author contains "Alice"
//	year >= 2020             →  year >= 2020
//	tag IN ("a", "b")        →  tag in ("a", "b")
//	NOT (year >= 2020)       →  !(year >= 2020)
type visitor struct {
	err            error
	sql            strings.Builder
	metadataPrefix string
}

func newVisitor(metadataPrefix string) *visitor {
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
			Has:        v.visitHasExpr,
			Like:       v.visitLikeExpr,
		})
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	default:
		return fmt.Errorf("vespa: unsupported root expression %T", node)
	}
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	lit, err := expr.Literal()
	if err != nil {
		return err
	}
	term, err := yqlLiteral(lit)
	if err != nil {
		return err
	}
	v.sql.WriteString(field)
	v.sql.WriteString(" contains ")
	v.sql.WriteString(term)
	return nil
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if !expr.Operator().Is(filter.OpNot) {
		return fmt.Errorf("vespa: unsupported unary '%s'", expr.Operator().String())
	}
	v.sql.WriteString("!(")
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
	field, err := v.fieldPath(expr)
	if err != nil {
		return err
	}
	lit, err := expr.Literal()
	if err != nil {
		return err
	}
	term, err := yqlLiteral(lit)
	if err != nil {
		return err
	}

	// String equality maps onto YQL `contains`; ordering / non-eq
	// numeric ops use the standard relational operators.
	if lit.IsString() && expr.Operator().Is(filter.OpEqual) {
		v.sql.WriteString(field)
		v.sql.WriteString(" contains ")
		v.sql.WriteString(term)
		return nil
	}
	if lit.IsString() && expr.Operator().Is(filter.OpNotEqual) {
		v.sql.WriteString("!(")
		v.sql.WriteString(field)
		v.sql.WriteString(" contains ")
		v.sql.WriteString(term)
		v.sql.WriteString(")")
		return nil
	}

	op, err := yqlOpFor(expr.Operator())
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
		return errors.New("vespa: 'IN' requires a list on the right")
	}
	if listLit.Len() == 0 {
		return errors.New("vespa: 'IN' requires a non-empty list")
	}
	parts := make([]string, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		term, err := yqlLiteral(lit)
		if err != nil {
			return err
		}
		parts = append(parts, term)
	}
	v.sql.WriteString(field)
	v.sql.WriteString(" in (")
	v.sql.WriteString(strings.Join(parts, ", "))
	v.sql.WriteByte(')')
	return nil
}

// visitLikeExpr maps SQL LIKE onto YQL `matches` (regex). `%` and
// `_` translate to `.*` / `.` respectively.
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
		return fmt.Errorf("vespa: LIKE requires a string pattern, got %T", value)
	}
	var b strings.Builder
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteByte('.')
		case '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	v.sql.WriteString(field)
	v.sql.WriteString(" matches ")
	v.sql.WriteString(quoteYQLString(b.String()))
	return nil
}

func (v *visitor) fieldPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.IdentifierPath()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("vespa: empty key path")
	}
	joined := strings.Join(keys, ".")
	if v.metadataPrefix == "" {
		return joined, nil
	}
	return v.metadataPrefix + "." + joined, nil
}

func yqlOpFor(kind filter.Operator) (string, error) {
	switch kind {
	case filter.OpEqual:
		return "=", nil
	case filter.OpNotEqual:
		return "!=", nil
	case filter.OpLess:
		return "<", nil
	case filter.OpLessEqual:
		return "<=", nil
	case filter.OpGreater:
		return ">", nil
	case filter.OpGreaterEqual:
		return ">=", nil
	default:
		return "", fmt.Errorf("vespa: unexpected operator '%s'", kind.Name())
	}
}

// yqlLiteral renders a filter literal as a YQL term.
//
// It reads the literal rather than a scalar decoded out of it because the
// literal owns the exact numeral and a scalar cannot carry it back. Deciding
// integer-ness with float64(int64(value)) == value asked Go for an
// out-of-range float-to-int conversion, which the spec leaves
// implementation-defined: at 2^63 arm64 saturates to MaxInt64, whose float64
// compares equal, so the term became 9223372036854775807 while amd64 emitted
// the right digits. An integer past int64 had no branch at all and fell
// through to a %v rendering nobody owned.
func yqlLiteral(lit *filter.Literal) (string, error) {
	switch {
	case lit.IsString():
		text, err := lit.AsString()
		if err != nil {
			return "", fmt.Errorf("vespa: %w (at %s)", err, lit.Start().String())
		}
		return quoteYQLString(text), nil
	case lit.IsNumber():
		text, err := lit.NumberText()
		if err != nil {
			return "", fmt.Errorf("vespa: %w (at %s)", err, lit.Start().String())
		}
		return text, nil
	case lit.IsBool():
		value, err := lit.AsBool()
		if err != nil {
			return "", fmt.Errorf("vespa: %w (at %s)", err, lit.Start().String())
		}
		return strconv.FormatBool(value), nil
	default:
		return "", fmt.Errorf("vespa: unsupported literal kind '%s' at %s",
			lit.Kind(), lit.Start().String())
	}
}

func quoteYQLString(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}
