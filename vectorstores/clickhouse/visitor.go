package clickhouse

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a ClickHouse WHERE
// fragment. Metadata is stored as a `Map(String, String)` column;
// metadata keys are addressed with the map-subscript syntax
// (metadata['key']).
//
// Output shape:
//
//	author == "Alice"           →  metadata['author'] = ?
//	year >= 2020                →  (mapContains(metadata, 'year') AND
//	                                toDecimal128OrNull(metadata['year'], 18) >= ?)
//	tag IN ("a", "b")           →  metadata['tag'] IN (?, ?)
//	NOT (author == "Alice")     →  NOT (metadata['author'] = ?)
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
		return errors.New("clickhouse: cannot process nil expression")
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
		return fmt.Errorf("clickhouse: unsupported root expression %T", node)
	}
}

func (v *visitor) visitBinaryExpr(expr *filter.BinaryExpr) error {
	switch {
	case expr.Operator().IsLogicalOperator():
		return v.visitLogicalExpr(expr)
	case expr.Operator().Is(filter.OpIn):
		return v.visitInExpr(expr)
	case expr.Operator().Is(filter.OpHas):
		return fmt.Errorf("clickhouse: HAS is not supported because metadata is stored as Map(String, String) at %s",
			expr.Start().String())
	case expr.Operator().Is(filter.OpLike):
		return v.visitLikeExpr(expr)
	case expr.Operator().IsEqualityOperator() || expr.Operator().IsOrderingOperator():
		return v.visitComparisonExpr(expr)
	default:
		return fmt.Errorf("clickhouse: unsupported binary operator '%s'", expr.Operator().String())
	}
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	if !expr.Operator().Is(filter.OpNot) {
		return fmt.Errorf("clickhouse: unsupported unary '%s'", expr.Operator().String())
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
// assigns an absent metadata key, so the leaf is never NULL and never leans on
// a Map's type default.
//
// A Map(String, String) subscript answers an absent key with the empty string,
// which decided string comparisons correctly by accident but made
// `metadata['k'] == ”` true for a key that is not there — the AST reads that
// as nil, which is not equal to the empty string. The numeric path now yields
// NULL for an absent key instead of inventing a zero, and NULL stays NULL
// under NOT, which would drop the rows a negated filter should keep.
// mapContains asks the question directly. The caller closes the parenthesis
// opened here.
func (v *visitor) appendAbsentGuard(key string, absentMatches bool) {
	v.sql.WriteByte('(')
	if absentMatches {
		v.sql.WriteString("NOT ")
	}
	v.sql.WriteString("mapContains(")
	v.sql.WriteString(v.metadataColumn)
	v.sql.WriteString(", ")
	v.sql.WriteString(quoteSQLString(key))
	v.sql.WriteString(")")
	if absentMatches {
		v.sql.WriteString(" OR ")
	} else {
		v.sql.WriteString(" AND ")
	}
}

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildKeyPath(expr)
	if err != nil {
		return fmt.Errorf("clickhouse: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("clickhouse: %w (at %s)", err, expr.Start().String())
	}
	op, err := sqlOpFor(expr.Operator())
	if err != nil {
		return err
	}
	v.appendAbsentGuard(jsonPath, expr.Operator() == filter.OpNotEqual)
	v.appendMapAccess(jsonPath, value, expr.Operator())
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	v.appendValuePlaceholder(value)
	v.sql.WriteByte(')')
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	jsonPath, err := buildKeyPath(expr)
	if err != nil {
		return fmt.Errorf("clickhouse: %w (at %s)", err, expr.Start().String())
	}
	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return errors.New("clickhouse: 'IN' requires a list on the right")
	}
	if listLit.Len() == 0 {
		return errors.New("clickhouse: 'IN' requires a non-empty list")
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
	v.appendMapAccess(jsonPath, values[0], filter.OpEqual)
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
	jsonPath, err := buildKeyPath(expr)
	if err != nil {
		return fmt.Errorf("clickhouse: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("clickhouse: %w (at %s)", err, expr.Start().String())
	}
	pattern, ok := value.(string)
	if !ok {
		return fmt.Errorf("clickhouse: LIKE requires a string pattern, got %T", value)
	}
	v.appendAbsentGuard(jsonPath, false)
	v.appendMapAccess(jsonPath, "", filter.OpEqual)
	v.sql.WriteString(" LIKE ")
	v.appendValuePlaceholder(pattern)
	v.sql.WriteByte(')')
	return nil
}

// visitNullTestExpr emits `NOT mapContains(metadata, 'key')` for an
// `IS NULL` test. ClickHouse stores metadata as a `Map(String, String)`,
// where a subscript on a missing key yields the type default (empty
// string) rather than SQL NULL — so `metadata['key'] IS NULL` can never
// match. Key absence is therefore the right model for "is null", which
// matches the inmemory reference semantics (a missing metadata key reads
// as null). The negated `IS NOT NULL` arrives as NOT(… IS NULL) and is
// rendered by visitUnaryExpr, so no separate handling is needed here.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	key, err := buildKeyPath(expr)
	if err != nil {
		return fmt.Errorf("clickhouse: %w (at %s)", err, expr.Start().String())
	}
	v.sql.WriteString("(NOT mapContains(")
	v.sql.WriteString(v.metadataColumn)
	v.sql.WriteString(", ")
	v.sql.WriteString(quoteSQLString(key))
	v.sql.WriteString("))")
	return nil
}

// metadataNumericScale leaves twenty integer digits inside Decimal128's
// documented 38, which covers every integer the filter AST can carry.
const metadataNumericScale = 18

// appendMapAccess writes `metadata['key']`, converting the access to an exact
// decimal when the comparison value implies numeric semantics.
//
// toFloat64OrZero was wrong twice over. Float64's 53-bit mantissa cannot hold
// every int64, so an id past 2^53 compared equal to its neighbor; and OrZero
// turns a value it cannot parse into 0, so `metadata['missing'] > -1` matched a
// row that has no such key — a Map(String, String) subscript yields the empty
// string for an absent key. toDecimal128OrNull is exact and yields NULL for
// both cases, which the leaf guard then resolves to the truth value the AST
// assigns an absent key. A present but non-numeric value also becomes NULL and
// drops the row; the AST reports that case as an error instead, so there is no
// decided answer for the server to disagree with.
func (v *visitor) appendMapAccess(key string, value any, op filter.Operator) {
	switch value.(type) {
	case float64, int64, uint64, int:
		v.sql.WriteString("toDecimal128OrNull(")
		v.sql.WriteString(v.metadataColumn)
		v.sql.WriteString("[")
		v.sql.WriteString(quoteSQLString(key))
		v.sql.WriteString("], ")
		v.sql.WriteString(strconv.Itoa(metadataNumericScale))
		v.sql.WriteString(")")
	default:
		if op.IsOrderingOperator() {
			v.sql.WriteString("toFloat64OrZero(")
			v.sql.WriteString(v.metadataColumn)
			v.sql.WriteString("[")
			v.sql.WriteString(quoteSQLString(key))
			v.sql.WriteString("])")
		} else {
			v.sql.WriteString(v.metadataColumn)
			v.sql.WriteString("[")
			v.sql.WriteString(quoteSQLString(key))
			v.sql.WriteString("]")
		}
	}
}

func (v *visitor) appendValuePlaceholder(value any) {
	if b, ok := value.(bool); ok {
		// ClickHouse Map(String, String) — booleans render as text.
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

func buildKeyPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.Path()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("clickhouse: empty key path")
	}
	return strings.Join(keys, "."), nil
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
		return "", fmt.Errorf("clickhouse: unexpected comparison operator '%s'", kind.Name())
	}
}

func quoteSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
