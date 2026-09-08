package couchbase

import (
	"errors"
	"fmt"
	"strings"

	"encoding/json"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a SQL++ (N1QL)
// predicate fragment usable in `WHERE` clauses of queries and
// DELETE statements.
//
// Output shape (metadata keys are addressed under metadata.*):
//
//	author == "Alice"          →  metadata.`author` = "Alice"
//	year >= 2020               →  metadata.`year` >= 2020
//	category IN ("a", "b")     →  metadata.`category` IN ["a", "b"]
//	NOT (year >= 2020)         →  NOT (metadata.`year` >= 2020)
//	a == "x" AND b == "y"      →  (metadata.`a` = "x" AND metadata.`b` = "y")
//
// Values are JSON-encoded — strings get double-quoted with embedded
// quotes escaped per JSON rules, which is also valid in SQL++ string
// literals.
type visitor struct {
	err            error
	sql            strings.Builder
	metadataPrefix string // typically "metadata"
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
	v.err = nil
	v.sql.Reset()
	v.err = v.visit(expr)
	return v.err
}

func (v *visitor) visit(expr filter.Expr) error {
	if expr == nil {
		return errors.New("couchbase: cannot process nil expression")
	}
	if v.err != nil {
		return v.err
	}

	switch node := expr.(type) {
	case *filter.BinaryExpr:
		if node.Operator().IsNullOperator() {
			return v.visitNullTestExpr(node)
		}
		return v.visitBinaryExpr(node)
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	default:
		return fmt.Errorf("couchbase: unsupported root expression %T", node)
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
	case expr.Operator().IsEqualityOperator() || expr.Operator().IsOrderingOperator():
		return v.visitComparisonExpr(expr)
	default:
		return fmt.Errorf("couchbase: unsupported binary operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
	}
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("couchbase: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("couchbase: %w (at %s)", err, expr.Start().String())
	}

	v.sql.WriteString("ANY element IN ")
	v.sql.WriteString(field)
	v.sql.WriteString(" SATISFIES element = ")
	literal, literalErr := jsonValue(value)
	if literalErr != nil {
		return literalErr
	}
	v.sql.WriteString(literal)
	v.sql.WriteString(" END")
	return nil
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if !expr.Operator().Is(filter.OpNot) {
		return fmt.Errorf("couchbase: unsupported unary operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
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

// appendAbsentGuard opens a leaf predicate with the truth value the filter AST
// assigns an absent metadata key, so the leaf is never MISSING.
//
// SQL++ says that "if either operand in a comparison is MISSING, the result is
// MISSING", which drops the document for any operator and stays MISSING under
// NOT — dropping the documents a negated filter is supposed to keep. The AST is
// two-valued: an absent key evaluates as nil and filter.Match decides every
// comparison against it, false for ==, ordering, LIKE and IN, true for !=.
//
// IS VALUED is the test that separates the two cases: it is false for both
// MISSING and NULL, which is exactly how the AST reads an absent key. The
// caller closes the parenthesis opened here.
func (v *visitor) appendAbsentGuard(field string, absentMatches bool) {
	v.sql.WriteByte('(')
	v.sql.WriteString(field)
	if absentMatches {
		v.sql.WriteString(" IS NOT VALUED OR ")
	} else {
		v.sql.WriteString(" IS VALUED AND ")
	}
}

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("couchbase: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("couchbase: %w (at %s)", err, expr.Start().String())
	}
	op, err := sqlOpFor(expr.Operator())
	if err != nil {
		return err
	}

	v.appendAbsentGuard(field, expr.Operator() == filter.OpNotEqual)
	v.sql.WriteString(field)
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	literal, literalErr := jsonValue(value)
	if literalErr != nil {
		return literalErr
	}
	v.sql.WriteString(literal)
	v.sql.WriteByte(')')
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("couchbase: %w (at %s)", err, expr.Start().String())
	}

	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return fmt.Errorf("couchbase: 'IN' requires a list on the right at %s, got %T",
			expr.Start().String(), expr.Right())
	}
	if listLit.Len() == 0 {
		return fmt.Errorf("couchbase: 'IN' requires a non-empty list at %s",
			expr.Start().String())
	}

	values := make([]any, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return fmt.Errorf("couchbase: %w (at %s)", err, expr.Start().String())
		}
		values = append(values, val)
	}

	v.appendAbsentGuard(field, false)
	v.sql.WriteString(field)
	v.sql.WriteString(" IN ")
	literal, literalErr := jsonValue(values)
	if literalErr != nil {
		return literalErr
	}
	v.sql.WriteString(literal)
	v.sql.WriteByte(')')
	return nil
}

// visitLikeExpr emits SQL++ LIKE — SQL wildcards % / _ pass through
// untouched since LIKE uses the same syntax.
func (v *visitor) visitLikeExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("couchbase: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("couchbase: %w (at %s)", err, expr.Start().String())
	}
	pattern, ok := value.(string)
	if !ok {
		return fmt.Errorf("couchbase: LIKE requires a string pattern, got %T at %s",
			value, expr.Start().String())
	}

	v.appendAbsentGuard(field, false)
	v.sql.WriteString(field)
	v.sql.WriteString(" LIKE ")
	literal, literalErr := jsonValue(pattern)
	if literalErr != nil {
		return literalErr
	}
	v.sql.WriteString(literal)
	v.sql.WriteByte(')')
	return nil
}

// visitNullTestExpr emits `(<path> IS NOT VALUED)`.
//
// SQL++ separates the states: "IS NULL: Field has value of NULL" and "IS
// MISSING: No value for field found". An absent key is MISSING, so IS NULL
// does not match it — and an absent key is the ordinary case for metadata.
// IS NOT VALUED is false for a real value and true for both NULL and MISSING,
// which is how the AST reads an absent key. The negated IS NOT NULL arrives as
// NOT(...) and is rendered by visitUnaryExpr, so no separate handling is
// needed here. No bound parameter is required.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	field, err := v.fieldPath(expr)
	if err != nil {
		return fmt.Errorf("couchbase: %w (at %s)", err, expr.Start().String())
	}
	v.sql.WriteString("(")
	v.sql.WriteString(field)
	v.sql.WriteString(" IS NOT VALUED)")
	return nil
}

// fieldPath builds the dotted SQL++ path for the left operand, with
// each segment backtick-quoted to allow special characters.
func (v *visitor) fieldPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.Path()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("empty key path on left operand")
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, "`"+strings.ReplaceAll(k, "`", "``")+"`")
	}
	joined := strings.Join(parts, ".")
	if v.metadataPrefix == "" {
		return joined, nil
	}
	return v.metadataPrefix + "." + joined, nil
}

func sqlOpFor(kind filter.Operator) (string, error) {
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
		return "", fmt.Errorf("unexpected comparison operator '%s'", kind.Name())
	}
}

// jsonValue renders a filter value as the JSON literal SQL++ reads.
//
// Encoding to JSON is exactly right here rather than convenient: SQL++ is a
// JSON query language, so a JSON number is the exact numeral and a JSON string
// carries JSON's own escaping.
//
// A marshal failure is reported instead of falling back to "null". That
// fallback was worse than an obviously broken value: null is a valid SQL++
// literal, so `field = null` compiled cleanly and evaluated to unknown, which
// drops the row. A failure to encode the caller's value became a filter that
// silently matches nothing.
func jsonValue(v any) (string, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("couchbase: encode filter value of type %T: %w", v, err)
	}
	return string(encoded), nil
}
