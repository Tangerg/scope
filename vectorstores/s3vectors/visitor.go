package s3vectors

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into the JSON filter
// document S3 Vectors expects under the QueryVectors `Filter` field.
//
// The S3 Vectors filter language is Mongo-flavored:
//
//	author == "Alice"        →  {"author": {"$eq": "Alice"}}
//	year >= 2020             →  {"year":   {"$gte": 2020}}
//	tag IN ("a", "b")        →  {"tag":    {"$in": ["a", "b"]}}
//	NOT (author == "Alice")  →  {"$not":   {"author": {"$eq": "Alice"}}}
//	a == "x" AND b == "y"    →  {"$and": [{"a":{"$eq":"x"}}, {"b":{"$eq":"y"}}]}
type visitor struct {
	err    error
	result map[string]any
}

func newVisitor() *visitor { return &visitor{} }

func (v *visitor) snapshot() map[string]any {
	if v.err != nil {
		return nil
	}
	return v.result
}

func (v *visitor) Visit(expr filter.Predicate) error {
	doc, err := v.translate(expr)
	v.err = err
	v.result = doc
	return v.err
}

func (v *visitor) translate(expr filter.Expr) (map[string]any, error) {
	switch node := expr.(type) {
	case *filter.BinaryExpr:
		return v.translateBinary(node)
	case *filter.UnaryExpr:
		return v.translateUnary(node)
	default:
		return nil, fmt.Errorf("s3vectors: unsupported root expression %T", node)
	}
}

func (v *visitor) translateBinary(expr *filter.BinaryExpr) (map[string]any, error) {
	switch {
	case expr.Operator().IsLogicalOperator():
		return v.translateLogical(expr)
	case expr.Operator().Is(filter.OpIn):
		return v.translateIn(expr)
	case expr.Operator().Is(filter.OpHas):
		return v.translateHas(expr)
	case expr.Operator().IsNullOperator():
		return v.translateNullTest(expr)
	case expr.Operator().IsEqualityOperator() || expr.Operator().IsOrderingOperator():
		return v.translateComparison(expr)
	default:
		return nil, fmt.Errorf("s3vectors: unsupported binary operator '%s'", expr.Operator().String())
	}
}

// translateHas uses S3 Vectors' documented scalar equality behavior for
// metadata arrays: $eq matches when any array element equals the scalar.
func (v *visitor) translateHas(expr *filter.BinaryExpr) (map[string]any, error) {
	key, err := keyName(expr.Left())
	if err != nil {
		return nil, err
	}
	value, err := expr.Value()
	if err != nil {
		return nil, err
	}
	return map[string]any{key: map[string]any{"$eq": value}}, nil
}

func (v *visitor) translateUnary(expr *filter.UnaryExpr) (map[string]any, error) {
	if !expr.Operator().Is(filter.OpNot) {
		return nil, fmt.Errorf("s3vectors: unsupported unary '%s'", expr.Operator().String())
	}
	inner, err := v.translate(expr.Right())
	if err != nil {
		return nil, err
	}
	return map[string]any{"$not": inner}, nil
}

func (v *visitor) translateLogical(expr *filter.BinaryExpr) (map[string]any, error) {
	left, err := v.translate(expr.Left())
	if err != nil {
		return nil, err
	}
	right, err := v.translate(expr.Right())
	if err != nil {
		return nil, err
	}
	op := "$and"
	if expr.Operator().Is(filter.OpOr) {
		op = "$or"
	}
	return map[string]any{op: []any{left, right}}, nil
}

func (v *visitor) translateComparison(expr *filter.BinaryExpr) (map[string]any, error) {
	key, err := keyName(expr.Left())
	if err != nil {
		return nil, err
	}
	value, err := expr.Value()
	if err != nil {
		return nil, err
	}
	op, err := mongoOpFor(expr.Operator())
	if err != nil {
		return nil, err
	}
	return map[string]any{key: map[string]any{op: value}}, nil
}

func (v *visitor) translateIn(expr *filter.BinaryExpr) (map[string]any, error) {
	key, err := keyName(expr.Left())
	if err != nil {
		return nil, err
	}
	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return nil, errors.New("s3vectors: 'IN' requires a list on the right")
	}
	if listLit.Len() == 0 {
		return nil, errors.New("s3vectors: 'IN' requires a non-empty list")
	}
	values := make([]any, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return nil, err
		}
		values = append(values, val)
	}
	return map[string]any{key: map[string]any{"$in": values}}, nil
}

// translateNullTest emits $exists, which S3 Vectors documents as checking
// whether the key is present "regardless of the value that's stored". Its
// filterable metadata holds strings, numbers, booleans and lists and cannot
// hold null, so an absent key is the only null-ish state — making
// $exists: false exactly the filter AST's IS NULL.
func (v *visitor) translateNullTest(expr *filter.BinaryExpr) (map[string]any, error) {
	key, err := keyName(expr.Left())
	if err != nil {
		return nil, fmt.Errorf("s3vectors: extract field key from 'IS NULL': %w", err)
	}
	return map[string]any{key: map[string]any{"$exists": false}}, nil
}

func keyName(expr filter.Expr) (string, error) {
	switch node := expr.(type) {
	case *filter.Ident:
		return node.Name(), nil
	case *filter.IndexExpr:
		keys, err := node.Path()
		if err != nil {
			return "", err
		}
		// S3 Vectors filters address flat metadata keys.
		return strings.Join(keys, "."), nil
	default:
		return "", fmt.Errorf("unsupported left operand %T", node)
	}
}

func mongoOpFor(kind filter.Operator) (string, error) {
	switch kind {
	case filter.OpEqual:
		return "$eq", nil
	case filter.OpNotEqual:
		return "$ne", nil
	case filter.OpLess:
		return "$lt", nil
	case filter.OpLessEqual:
		return "$lte", nil
	case filter.OpGreater:
		return "$gt", nil
	case filter.OpGreaterEqual:
		return "$gte", nil
	default:
		return "", fmt.Errorf("unexpected operator '%s'", kind.Name())
	}
}
