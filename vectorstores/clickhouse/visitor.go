package clickhouse

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a ClickHouse WHERE
// fragment. Metadata is stored as a `Map(String, String)` column holding each
// value's JSON text; metadata keys are addressed with the map-subscript syntax
// (metadata['key']).
//
// Every leaf opens with the truth value the AST assigns a key it reads as nil,
// which is both an absent key and a stored null:
//
//	author == 'Alice'   →  (mapContains(metadata, 'author')
//	                        AND metadata['author'] != 'null'
//	                        AND metadata['author'] = ?)          arg: "Alice"
//	year >= 2020        →  (mapContains(metadata, 'year')
//	                        AND metadata['year'] != 'null'
//	                        AND toDecimal128OrNull(metadata['year'], 18) >= ?)
//	author != 'Alice'   →  (NOT mapContains(metadata, 'author')
//	                        OR metadata['author'] = 'null'
//	                        OR metadata['author'] <> ?)
//	author is null      →  (NOT mapContains(metadata, 'author')
//	                        OR metadata['author'] = 'null')
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
	v.sql.Reset()
	v.args = nil
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
				return fmt.Errorf("clickhouse: HAS is not supported because metadata is stored as Map(String, String) at %s",
					expr.Start().String())
			},
			Like:     v.visitLikeExpr,
			NullTest: v.visitNullTestExpr,
		})
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	default:
		return fmt.Errorf("clickhouse: unsupported root expression %T", node)
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

// jsonNull is the stored text of a metadata value that is JSON null. Metadata
// is stored as JSON text, so a null value is present in the map with this
// text; the filter AST reads it as nil, the same as a key that is not there.
const jsonNull = "'null'"

// appendAbsentGuard opens a leaf predicate with the truth value the filter AST
// assigns a key it reads as nil, so the leaf is never NULL and never leans on
// a Map's type default.
//
// The AST reads both an absent key and a null value as nil, so a total leaf
// has to answer for both. A Map(String, String) subscript answers an absent key
// with the empty string, which decided string comparisons correctly by accident
// but made `metadata['k'] == ”` true for a key that is not there. The numeric
// path yields NULL for an absent key instead of inventing a zero, and NULL
// stays NULL under NOT, which would drop the rows a negated filter should keep.
// mapContains and the stored null text ask both questions directly. The caller
// closes the parenthesis opened here.
func (v *visitor) appendAbsentGuard(key string, absentMatches bool) {
	v.sql.WriteByte('(')
	if absentMatches {
		v.sql.WriteString("NOT ")
	}
	v.appendMapContains(key)
	if absentMatches {
		v.sql.WriteString(" OR ")
		v.appendMapSubscript(key)
		v.sql.WriteString(" = ")
		v.sql.WriteString(jsonNull)
		v.sql.WriteString(" OR ")
		return
	}
	v.sql.WriteString(" AND ")
	v.appendMapSubscript(key)
	v.sql.WriteString(" != ")
	v.sql.WriteString(jsonNull)
	v.sql.WriteString(" AND ")
}

func (v *visitor) appendMapContains(key string) {
	v.sql.WriteString("mapContains(")
	v.sql.WriteString(v.metadataColumn)
	v.sql.WriteString(", ")
	v.sql.WriteString(quoteSQLString(key))
	v.sql.WriteString(")")
}

func (v *visitor) appendMapSubscript(key string) {
	v.sql.WriteString(v.metadataColumn)
	v.sql.WriteString("[")
	v.sql.WriteString(quoteSQLString(key))
	v.sql.WriteString("]")
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
	v.appendMapAccess(jsonPath, value)
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteByte(' ')
	if err := v.appendValuePlaceholder(value); err != nil {
		return err
	}
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
	v.appendMapAccess(jsonPath, values[0])
	v.sql.WriteString(" IN (")
	for i, val := range values {
		if i > 0 {
			v.sql.WriteString(", ")
		}
		if err := v.appendValuePlaceholder(val); err != nil {
			return err
		}
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
	v.appendMapAccess(jsonPath, "")
	v.sql.WriteString(" LIKE ")
	// A string is stored as its JSON text, so the stored value carries the
	// surrounding quotes. OpLike matches the whole value rather than a
	// substring of it, so quoting the pattern the same way keeps the match
	// anchored where the operator says it is.
	v.args = append(v.args, `"`+pattern+`"`)
	v.sql.WriteByte('?')
	v.sql.WriteByte(')')
	return nil
}

// visitNullTestExpr answers `IS NULL` for both states the filter AST reads as
// nil: a key that is not in the map, and a key whose stored JSON text is null.
// ClickHouse stores metadata as a `Map(String, String)`, where a subscript on a
// missing key yields the type default (empty string) rather than SQL NULL — so
// `metadata['key'] IS NULL` can never match and the question has to be asked of
// the map itself. The negated `IS NOT NULL` arrives as NOT(… IS NULL) and is
// rendered by visitUnaryExpr, so no separate handling is needed here.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	key, err := buildKeyPath(expr)
	if err != nil {
		return fmt.Errorf("clickhouse: %w (at %s)", err, expr.Start().String())
	}
	v.sql.WriteString("(NOT ")
	v.appendMapContains(key)
	v.sql.WriteString(" OR ")
	v.appendMapSubscript(key)
	v.sql.WriteString(" = ")
	v.sql.WriteString(jsonNull)
	v.sql.WriteString(")")
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
// assigns nil. A present but non-numeric value also becomes NULL and drops the
// row; the AST reports that case as an error instead, so there is no decided
// answer for the server to disagree with.
//
// Every other access is a bare subscript compared as text. A string is stored
// as its JSON text, and prefixing every string with the same quote leaves
// lexicographic order unchanged, so text comparison stays faithful.
func (v *visitor) appendMapAccess(key string, value any) {
	switch value.(type) {
	case float64, int64, uint64, int:
		v.sql.WriteString("toDecimal128OrNull(")
		v.appendMapSubscript(key)
		v.sql.WriteString(", ")
		v.sql.WriteString(strconv.Itoa(metadataNumericScale))
		v.sql.WriteString(")")
	default:
		v.appendMapSubscript(key)
	}
}

// appendValuePlaceholder binds the value that the stored text is compared
// against.
//
// Metadata is stored as JSON text, so a string and a bool bind their JSON
// encoding — the quotes around a stored string are part of the stored value,
// and binding the bare text would compare `Alice` against `"Alice"` and match
// nothing. A number is reached through toDecimal128OrNull, which yields a
// Decimal, so it binds as a number rather than as text.
func (v *visitor) appendValuePlaceholder(value any) error {
	switch value.(type) {
	case string, bool:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("clickhouse: encode filter value of type %T: %w", value, err)
		}
		v.args = append(v.args, string(encoded))
	default:
		v.args = append(v.args, value)
	}
	v.sql.WriteByte('?')
	return nil
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
