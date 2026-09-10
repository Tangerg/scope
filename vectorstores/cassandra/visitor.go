package cassandra

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into a CQL WHERE
// fragment. Each metadata key must map to an actual indexed column on
// the underlying table — Cassandra has no JSON-path operator, so
// filters reference columns directly.
//
// Output shape:
//
//	author == "Alice"          →  "author" = ?
//	year >= 2020               →  "year" >= ?
//	tag IN ("a", "b")          →  "tag" IN ?
//
// IN values are bound as a single slice parameter so callers can pass
// `[]string{"a", "b"}` straight through.
type visitor struct {
	err  error
	sql  strings.Builder
	args []any
}

func newVisitor() *visitor { return &visitor{} }

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
				return fmt.Errorf("cassandra: HAS requires a declared collection column type, which this scalar metadata schema does not provide at %s",
					expr.Start().String())
			},
		})
	case *filter.UnaryExpr:
		return errors.New("cassandra: NOT is not supported by CQL on metadata columns")
	default:
		return fmt.Errorf("cassandra: unsupported root expression %T", node)
	}
}

func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	if expr.Operator().Is(filter.OpOr) {
		return errors.New("cassandra: OR is not supported in CQL WHERE clauses")
	}
	if err := v.visit(expr.Left()); err != nil {
		return err
	}
	v.sql.WriteString(" AND ")
	return v.visit(expr.Right())
}

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	column, err := columnName(expr.Left())
	if err != nil {
		return fmt.Errorf("cassandra: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return fmt.Errorf("cassandra: %w (at %s)", err, expr.Start().String())
	}
	op, err := cqlOpFor(expr.Operator())
	if err != nil {
		return err
	}

	v.sql.WriteString(quoteIdentifier(column))
	v.sql.WriteByte(' ')
	v.sql.WriteString(op)
	v.sql.WriteString(" ?")
	v.args = append(v.args, value)
	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	column, err := columnName(expr.Left())
	if err != nil {
		return fmt.Errorf("cassandra: %w (at %s)", err, expr.Start().String())
	}

	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return fmt.Errorf("cassandra: 'IN' requires a list on the right at %s, got %T",
			expr.Start().String(), expr.Right())
	}
	if listLit.Len() == 0 {
		return fmt.Errorf("cassandra: 'IN' requires a non-empty list at %s",
			expr.Start().String())
	}

	values, err := listToTypedSlice(listLit)
	if err != nil {
		return fmt.Errorf("cassandra: %w (at %s)", err, expr.Start().String())
	}

	v.sql.WriteString(quoteIdentifier(column))
	v.sql.WriteString(" IN ?")
	v.args = append(v.args, values)
	return nil
}

// columnName extracts the (single) column name from the left operand.
// Cassandra filters work on flat indexed columns — there's no JSON
// access — so an [filter.IndexExpr] is rejected.
func columnName(expr filter.Expr) (string, error) {
	switch node := expr.(type) {
	case *filter.Ident:
		return node.Name(), nil
	case *filter.IndexExpr:
		return "", errors.New("indexed expressions are not supported — declare the metadata key as a column")
	default:
		return "", fmt.Errorf("unsupported left operand %T", node)
	}
}

// listToTypedSlice promotes the literal list to a Go slice typed by
// the first element. gocql binds typed slices to `IN ?` parameters.
func listToTypedSlice(list *filter.ListLiteral) (any, error) {
	return list.Values()
}

func cqlOpFor(kind filter.Operator) (string, error) {
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
		return "", fmt.Errorf("unexpected operator '%s'", kind.Name())
	}
}

func quoteIdentifier(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
