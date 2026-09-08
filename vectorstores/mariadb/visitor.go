package mariadb

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a MariaDB WHERE
// fragment. Metadata is stored as JSON; the visitor reaches into it
// with JSON_VALUE plus a casting helper for numeric / boolean values
// so the comparison happens in the right SQL type.
//
// Output shape:
//
//	author == "Alice"          →  JSON_VALUE(metadata, '$.author') = ?
//	year >= 2020               →  CAST(JSON_VALUE(metadata, '$.year') AS DECIMAL(65,30)) >= ?
//	published == true          →  JSON_VALUE(metadata, '$.published') = 'true'  (params: nope — bools render inline)
//	tag IN ("a", "b")          →  JSON_VALUE(metadata, '$.tag') IN (?, ?)
//	NOT (a == "x")             →  NOT (JSON_VALUE(metadata, '$.a') = ?)
//
// Bool literals render inline because MariaDB doesn't accept a true
// Go bool through the binary protocol for a JSON-comparison context.
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
		return errors.New("mariadb: cannot process nil expression")
	}
	if v.err != nil {
		return v.err
	}

	switch node := expr.(type) {
	case *filter.BinaryExpr:
		if node.Operator().IsNullOperator() {
			return v.visitNullTestExpr(node)
		}
		return node.Dispatch(filter.BinaryHandlers{
			Logical:    v.visitLogicalExpr,
			Comparison: v.visitComparisonExpr,
			In:         v.visitInExpr,
			Has:        v.visitHasExpr,
			Like:       v.visitLikeExpr,
		})
	case *filter.UnaryExpr:
		return node.Dispatch(v.visitNotExpr)
	default:
		return fmt.Errorf("mariadb: unsupported root expression %T", node)
	}
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("mariadb: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("mariadb: %w (at %s)", err, expr.Start().String())
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

// appendJSONScalar writes a value as JSON_ARRAY input. Unlike JSON_VALUE
// comparison output, booleans must remain JSON booleans rather than strings.
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

func (v *visitor) visitNotExpr(expr *filter.UnaryExpr) error {
	v.sql.WriteString("NOT (")
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	op, err := expr.Operator().LogicalString()
	if err != nil {
		return fmt.Errorf("mariadb: %w", err)
	}
	v.sql.WriteString("(")
	if err := v.visit(expr.Left()); err != nil {
		return err
	}
	v.sql.WriteString(" ")
	v.sql.WriteString(op)
	v.sql.WriteString(" ")
	if err := v.visit(expr.Right()); err != nil {
		return err
	}
	v.sql.WriteString(")")
	return nil
}

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("mariadb: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("mariadb: %w (at %s)", err, expr.Start().String())
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
		return fmt.Errorf("mariadb: %w (at %s)", err, expr.Start().String())
	}

	listLit, err := expr.List()
	if err != nil {
		return fmt.Errorf("mariadb: %w", err)
	}

	values := make([]any, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return fmt.Errorf("mariadb: %w (at %s)", err, expr.Start().String())
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
		return fmt.Errorf("mariadb: %w (at %s)", err, expr.Start().String())
	}
	pattern, err := expr.Pattern()
	if err != nil {
		return fmt.Errorf("mariadb: %w", err)
	}

	v.appendAbsentGuard(jsonPath, false)
	v.appendJSONExtraction(jsonPath, "", filter.OpEqual)
	v.sql.WriteString(" LIKE ")
	v.appendValuePlaceholder(pattern)
	v.sql.WriteByte(')')
	return nil
}

// visitNullTestExpr emits `(JSON_VALUE(metadata, '$.key') IS NULL)`.
// JSON_VALUE yields SQL NULL both when the key is absent and when the
// stored value is JSON null, matching the inmemory reference semantics.
// The negated `IS NOT NULL` arrives as NOT(… IS NULL) and is rendered
// by visitNotExpr, so no separate handling is needed here.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("mariadb: %w (at %s)", err, expr.Start().String())
	}
	v.sql.WriteString("(JSON_VALUE(")
	v.sql.WriteString(v.metadataColumn)
	v.sql.WriteString(", ")
	v.sql.WriteString(quoteSQLString(jsonPath))
	v.sql.WriteString(") IS NULL)")
	return nil
}

// appendJSONExtraction emits the JSON_VALUE / CAST wrapper appropriate
// for the comparison's value type.
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
			// Ordering on non-numeric literals — force a numeric
			// cast so the comparison still has well-defined semantics.
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

// appendValuePlaceholder binds the value and writes a `?`. Booleans
// render inline as 'true' / 'false' since JSON_VALUE returns strings.
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
		return "", errors.New("mariadb: empty key path on left operand")
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
		return "", fmt.Errorf("mariadb: unexpected comparison operator '%s'", kind.Name())
	}
}

func quoteSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
