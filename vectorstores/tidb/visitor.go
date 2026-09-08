package tidb

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a TiDB WHERE
// fragment. TiDB stores metadata as JSON and the visitor reaches
// into it with JSON_VALUE + per-type casts so numeric / boolean
// comparisons happen in the right SQL type.
//
// Output shape (default metadata column "metadata"):
//
//	author == "Alice"        →  JSON_VALUE(metadata, '$.author') = ?
//	year >= 2020             →  CAST(JSON_VALUE(metadata, '$.year') AS DECIMAL(65,30)) >= ?
//	tag IN ("a", "b")        →  JSON_VALUE(metadata, '$.tag') IN (?, ?)
type visitor struct {
	err            error
	sql            strings.Builder
	args           []any
	metadataColumn string
}

func newVisitor(metadataColumn string) *visitor {
	if metadataColumn == "" {
		metadataColumn = "metadata"
	}
	return &visitor{metadataColumn: metadataColumn}
}

func (v *visitor) snapshot() (string, []any) {
	if v.err != nil {
		return "", nil
	}
	return v.sql.String(), v.args
}

func (v *visitor) Visit(expr filter.Predicate) error {
	v.err = nil
	v.sql.Reset()
	v.args = nil
	v.err = v.visit(expr)
	return v.err
}

func (v *visitor) visit(expr filter.Expr) error {
	if expr == nil {
		return errors.New("tidb: cannot process nil expression")
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
		return fmt.Errorf("tidb: unsupported root expression %T", node)
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
		return fmt.Errorf("tidb: unsupported binary operator '%s'", expr.Operator().String())
	}
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("tidb: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("tidb: %w (at %s)", err, expr.Start().String())
	}

	v.sql.WriteString("JSON_CONTAINS(")
	v.sql.WriteString(v.metadataColumn)
	v.sql.WriteString(", JSON_ARRAY(")
	v.appendJSONScalar(value)
	v.sql.WriteString("), ")
	v.sql.WriteString(quoteSQLString(jsonPath))
	v.sql.WriteByte(')')
	return nil
}

// appendJSONScalar writes a value as JSON_ARRAY input. Booleans stay JSON
// booleans instead of the strings returned by JSON_VALUE comparisons.
func (v *visitor) appendJSONScalar(value any) {
	if boolean, ok := value.(bool); ok {
		if boolean {
			v.sql.WriteString("true")
		} else {
			v.sql.WriteString("false")
		}
		return
	}
	v.args = append(v.args, value)
	v.sql.WriteByte('?')
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if !expr.Operator().Is(filter.OpNot) {
		return fmt.Errorf("tidb: unsupported unary '%s'", expr.Operator().String())
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
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("tidb: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("tidb: %w (at %s)", err, expr.Start().String())
	}
	op, err := sqlOpFor(expr.Operator())
	if err != nil {
		return err
	}
	v.appendAbsentGuard(jsonPath, expr.Operator() == filter.OpNotEqual)
	v.appendJSONExtraction(jsonPath, value, expr.Operator())
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	v.appendValuePlaceholder(value)
	v.sql.WriteByte(')')
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("tidb: %w (at %s)", err, expr.Start().String())
	}
	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return errors.New("tidb: 'IN' requires a list on the right")
	}
	if listLit.Len() == 0 {
		return errors.New("tidb: 'IN' requires a non-empty list")
	}
	values := make([]any, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return err
		}
		values = append(values, val)
	}
	v.appendAbsentGuard(jsonPath, false)
	v.appendJSONExtraction(jsonPath, values[0], filter.OpEqual)
	v.sql.WriteString(" IN (")
	for i, val := range values {
		if i > 0 {
			v.sql.WriteString(", ")
		}
		v.appendValuePlaceholder(val)
	}
	v.sql.WriteString("))")
	return nil
}

func (v *visitor) visitLikeExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("tidb: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("tidb: %w (at %s)", err, expr.Start().String())
	}
	pattern, ok := value.(string)
	if !ok {
		return fmt.Errorf("tidb: LIKE requires a string pattern, got %T", value)
	}
	v.appendAbsentGuard(jsonPath, false)
	v.appendJSONExtraction(jsonPath, "", filter.OpEqual)
	v.sql.WriteString(" LIKE ")
	v.appendValuePlaceholder(pattern)
	v.sql.WriteByte(')')
	return nil
}

// visitNullTestExpr emits `(JSON_VALUE(metadata, '$.key') IS NULL)`.
// TiDB's JSON_VALUE yields SQL NULL both when the key is absent and when
// the stored value is JSON null, matching the inmemory reference
// semantics. No bound parameter is needed. The negated `IS NOT NULL`
// arrives as NOT(… IS NULL) and is rendered by visitUnaryExpr, so no
// separate handling is needed here.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("tidb: %w (at %s)", err, expr.Start().String())
	}
	v.sql.WriteString("(JSON_VALUE(")
	v.sql.WriteString(v.metadataColumn)
	v.sql.WriteString(", ")
	v.sql.WriteString(quoteSQLString(jsonPath))
	v.sql.WriteString(") IS NULL)")
	return nil
}

// appendAbsentGuard opens a leaf predicate with the truth value the filter AST
// assigns an absent metadata key, so the leaf is never UNKNOWN.
//
// The AST is two-valued: an absent key evaluates as nil and filter.Match
// decides every comparison against it — false for ==, ordering, LIKE and IN,
// true for !=. SQL is three-valued, so a bare comparison on a missing key is
// UNKNOWN, which drops the row for any operator and, worse, stays UNKNOWN
// under NOT, dropping the rows a negated filter is supposed to keep.
//
// The guard reads the uncast extraction on purpose: a cast is applied in order
// to compare, and testing the raw value for NULL keeps the guard independent
// of whether that cast succeeds. The caller closes the parenthesis opened here.
// metadataNumericCast keeps a numeric comparison exact.
//
// DOUBLE is an approximate type: its 53-bit mantissa cannot hold every int64,
// so an id or timestamp past 2^53 compares equal to its neighbor and a filter
// silently matches the wrong row. DECIMAL stores "exact numeric data values",
// and 65 digits is the documented maximum, which covers every int64 the filter
// AST can carry — the AST itself compares as a rational precisely so an
// integer is never rounded to a float's precision.
const metadataNumericCast = "DECIMAL(65,30)"

func (v *visitor) appendAbsentGuard(jsonPath string, absentMatches bool) {
	v.sql.WriteByte('(')
	v.appendJSONExtraction(jsonPath, "", filter.OpEqual)
	if absentMatches {
		v.sql.WriteString(" IS NULL OR ")
	} else {
		v.sql.WriteString(" IS NOT NULL AND ")
	}
}

func (v *visitor) appendJSONExtraction(jsonPath string, value any, op filter.Operator) {
	switch value.(type) {
	case float64, int64, uint64, int:
		v.sql.WriteString("CAST(JSON_VALUE(")
		v.sql.WriteString(v.metadataColumn)
		v.sql.WriteString(", ")
		v.sql.WriteString(quoteSQLString(jsonPath))
		v.sql.WriteString(") AS " + metadataNumericCast + ")")
	default:
		if op.IsOrderingOperator() {
			v.sql.WriteString("CAST(JSON_VALUE(")
			v.sql.WriteString(v.metadataColumn)
			v.sql.WriteString(", ")
			v.sql.WriteString(quoteSQLString(jsonPath))
			v.sql.WriteString(") AS " + metadataNumericCast + ")")
		} else {
			v.sql.WriteString("JSON_VALUE(")
			v.sql.WriteString(v.metadataColumn)
			v.sql.WriteString(", ")
			v.sql.WriteString(quoteSQLString(jsonPath))
			v.sql.WriteByte(')')
		}
	}
}

func (v *visitor) appendValuePlaceholder(value any) {
	if b, ok := value.(bool); ok {
		if b {
			v.sql.WriteString("'true'")
		} else {
			v.sql.WriteString("'false'")
		}
		return
	}
	v.args = append(v.args, value)
	v.sql.WriteByte('?')
}

func buildJSONPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.Path()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("empty key path")
	}
	return "$." + strings.Join(keys, "."), nil
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
		return "", fmt.Errorf("unexpected comparison operator '%s'", kind.Name())
	}
}

func quoteSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
