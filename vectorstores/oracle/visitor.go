package oracle

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into an Oracle WHERE
// fragment that reaches into the JSON `metadata` column. Numeric and
// boolean comparisons use Oracle's typed json_value extractors so the
// comparison happens in the correct SQL type.
//
// Output shape:
//
//	author == "Alice"          →  json_value(metadata, '$.author') = :1
//	year >= 2020               →  json_value(metadata, '$.year' RETURNING NUMBER) >= :1
//	published == true          →  json_value(metadata, '$.published') = 'true'
//	tag IN ("a", "b")          →  json_value(metadata, '$.tag') IN (:1, :2)
//	NOT (a == "x")             →  NOT (json_value(metadata, '$.a') = :1)
type visitor struct {
	err            error
	sql            strings.Builder
	args           []any
	paramCount     int
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
	v.paramCount = 0
	v.err = v.visit(expr)
	return v.err
}

func (v *visitor) visit(expr filter.Expr) error {
	if expr == nil {
		return errors.New("oracle: cannot process nil expression")
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
		return fmt.Errorf("oracle: unsupported root expression %T", node)
	}
}

// visitHasExpr uses a JSON_EXISTS array filter and binds the member through a
// SQL/JSON variable. Oracle does not accept Boolean SQL/JSON variables, so that
// genuine provider limitation is rejected explicitly.
func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("oracle: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("oracle: %w (at %s)", err, expr.Start().String())
	}
	if _, ok := value.(bool); ok {
		return fmt.Errorf("oracle: HAS does not support boolean members because JSON_EXISTS PASSING cannot bind BOOLEAN at %s", expr.Start().String())
	}

	v.sql.WriteString("json_exists(")
	v.sql.WriteString(v.metadataColumn)
	v.sql.WriteString(", ")
	v.sql.WriteString(quoteSQLString(jsonPath + "[*]?(@ == $member)"))
	v.sql.WriteString(" PASSING ")
	v.appendValuePlaceholder(value)
	v.sql.WriteString(" AS \"member\")")
	return nil
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
		return fmt.Errorf("oracle: %w", err)
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
		return fmt.Errorf("oracle: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("oracle: %w (at %s)", err, expr.Start().String())
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
		return fmt.Errorf("oracle: %w (at %s)", err, expr.Start().String())
	}

	listLit, err := expr.List()
	if err != nil {
		return fmt.Errorf("oracle: %w", err)
	}

	values := make([]any, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return fmt.Errorf("oracle: %w (at %s)", err, expr.Start().String())
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
		return fmt.Errorf("oracle: %w (at %s)", err, expr.Start().String())
	}
	pattern, err := expr.Pattern()
	if err != nil {
		return fmt.Errorf("oracle: %w", err)
	}

	v.appendAbsentGuard(jsonPath, false)
	v.appendJSONExtraction(jsonPath, "", filter.OpEqual)
	v.sql.WriteString(" LIKE ")
	v.appendValuePlaceholder(pattern)
	v.sql.WriteByte(')')
	return nil
}

// visitNullTestExpr emits `(json_value(metadata, '$.key') IS NULL)`.
// Oracle's json_value yields SQL NULL both when the path is absent and
// when the stored value is JSON null, matching the inmemory reference
// semantics. The negated `IS NOT NULL` arrives as NOT(… IS NULL) and is
// rendered by visitNotExpr, so no separate handling is needed here.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildJSONPath(expr)
	if err != nil {
		return fmt.Errorf("oracle: %w (at %s)", err, expr.Start().String())
	}
	v.sql.WriteString("(json_value(")
	v.sql.WriteString(v.metadataColumn)
	v.sql.WriteString(", ")
	v.sql.WriteString(quoteSQLString(jsonPath))
	v.sql.WriteString(") IS NULL)")
	return nil
}

// appendJSONExtraction emits the appropriate json_value() expression
// for the value's type. Numeric / boolean comparisons use Oracle's
// typed RETURNING clause so the type round-trips correctly.
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
		v.sql.WriteString("json_value(")
		v.sql.WriteString(v.metadataColumn)
		v.sql.WriteString(", ")
		v.sql.WriteString(quoteSQLString(jsonPath))
		v.sql.WriteString(" RETURNING NUMBER)")
	default:
		if op.IsOrderingOperator() {
			v.sql.WriteString("json_value(")
			v.sql.WriteString(v.metadataColumn)
			v.sql.WriteString(", ")
			v.sql.WriteString(quoteSQLString(jsonPath))
			v.sql.WriteString(" RETURNING NUMBER)")
		} else {
			v.sql.WriteString("json_value(")
			v.sql.WriteString(v.metadataColumn)
			v.sql.WriteString(", ")
			v.sql.WriteString(quoteSQLString(jsonPath))
			v.sql.WriteByte(')')
		}
	}
}

// appendValuePlaceholder binds the value via :N and writes the
// placeholder.
func (v *visitor) appendValuePlaceholder(value any) {
	if b, ok := value.(bool); ok {
		if b {
			v.sql.WriteString("'true'")
		} else {
			v.sql.WriteString("'false'")
		}
		return
	}
	v.paramCount++
	v.args = append(v.args, value)
	v.sql.WriteByte(':')
	v.sql.WriteString(strconv.Itoa(v.paramCount))
}

func buildJSONPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.Path()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("empty key path on left operand")
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
