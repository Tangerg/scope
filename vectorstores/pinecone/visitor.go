package pinecone

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor compiles Scope filter expressions into Pinecone's metadata-filter
// document. A value is reusable: Visit replaces the previous result. Pinecone
// has no standalone NOT, so negation is lowered through exact inverse
// comparisons and De Morgan rewrites; unsupported LIKE expressions and values
// that protobuf cannot represent exactly are rejected rather than approximated.
type visitor struct {
	err               error
	condition         map[string]any
	result            *structpb.Struct
	currentFieldKey   string
	currentFieldValue any
}

func newVisitor() *visitor {
	return &visitor{}
}

// Failed compilation clears the prior value so a reused compiler cannot leak a stale filter.
func (v *visitor) snapshot() *structpb.Struct {
	if v.err != nil {
		return nil
	}
	return v.result
}

// Visit replaces prior state and accepts only trees Pinecone can represent
// without changing their meaning.
func (v *visitor) Visit(expr filter.Predicate) error {
	v.condition = nil
	v.result = nil
	v.currentFieldKey = ""
	v.currentFieldValue = nil
	if v.err = v.visit(expr); v.err != nil {
		return v.err
	}
	v.result, v.err = structpb.NewStruct(v.condition)
	return v.err
}

func (v *visitor) visit(expr filter.Expr) error {
	switch node := expr.(type) {
	case *filter.BinaryExpr:
		return v.visitBinaryExpr(node)
	case *filter.UnaryExpr:
		return v.visitUnaryExpr(node)
	case *filter.IndexExpr:
		return v.visitIndexExpr(node)
	case *filter.Ident:
		return v.visitIdent(node)
	case *filter.Literal:
		return v.visitLiteral(node)
	case *filter.ListLiteral:
		return v.visitListLiteral(node)
	default:
		return fmt.Errorf("pinecone: unsupported expression type %T", node)
	}
}

func (v *visitor) visitBinaryExpr(expr *filter.BinaryExpr) error {
	return expr.Dispatch(filter.BinaryHandlers{
		Logical:    v.visitLogicalExpr,
		Comparison: v.visitComparisonExpr,
		In:         v.visitInExpr,
		Has:        v.visitHasExpr,
		Like:       v.visitLikeExpr,
		NullTest:   v.visitNullTestExpr,
	})
}

// visitNullTestExpr emits Pinecone's $exists.
//
// Pinecone metadata holds strings, numbers, booleans and string lists, so a
// key is either present with a value or absent — there is no stored null. That
// makes $exists: false exactly the filter AST's IS NULL, which treats an absent
// key as the value nil.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("pinecone: extract field key from 'IS NULL' at %s: %w",
			expr.Start().String(), err)
	}
	v.condition = map[string]any{fieldKey: map[string]any{"$exists": false}}
	return nil
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("pinecone: extract collection field at %s: %w", expr.Start().String(), err)
	}
	fieldValue, err := v.extractFieldValue(expr.Right())
	if err != nil {
		return fmt.Errorf("pinecone: extract collection member at %s: %w", expr.Start().String(), err)
	}
	if _, ok := fieldValue.(string); !ok {
		return fmt.Errorf("pinecone: HAS requires a string member because Pinecone collection metadata is string-only at %s", expr.Start().String())
	}
	v.condition = map[string]any{fieldKey: map[string]any{"$eq": fieldValue}}
	return nil
}

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	if expr.Operator().IsEqualityOperator() {
		return v.visitEqualityExpr(expr)
	}
	return v.visitOrderingExpr(expr)
}

func (v *visitor) visitLikeExpr(expr *filter.BinaryExpr) error {
	return fmt.Errorf("pinecone: LIKE operator is not supported in Pinecone metadata filters at %s",
		expr.Start().String())
}

func (v *visitor) visitUnaryExpr(expr *filter.UnaryExpr) error {
	return expr.Dispatch(v.visitNotExpr)
}

func (v *visitor) visitIdent(ident *filter.Ident) error {
	v.currentFieldKey = ident.Name()
	return nil
}

func (v *visitor) visitLiteral(lit *filter.Literal) error {
	value, err := v.literalToValue(lit)
	if err != nil {
		return err
	}
	v.currentFieldValue = value
	return nil
}

func (v *visitor) visitListLiteral(list *filter.ListLiteral) error {
	values := make([]any, 0, list.Len())

	for i, lit := range list.Literals() {
		value, err := v.literalToValue(lit)
		if err != nil {
			return fmt.Errorf("pinecone: convert list element at index %d: %w", i, err)
		}
		values = append(values, value)
	}

	v.currentFieldValue = values
	return nil
}

func (v *visitor) visitIndexExpr(expr *filter.IndexExpr) error {
	fieldKey, err := v.buildIndexedFieldKey(expr)
	if err != nil {
		return fmt.Errorf("pinecone: build field path at %s: %w",
			expr.Start().String(), err)
	}
	v.currentFieldKey = fieldKey
	return nil
}

func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	left, err := v.buildNestedExpr(expr.Left())
	if err != nil {
		return fmt.Errorf("pinecone: process left operand of '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	right, err := v.buildNestedExpr(expr.Right())
	if err != nil {
		return fmt.Errorf("pinecone: process right operand of '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	switch expr.Operator() {
	case filter.OpAnd:
		v.condition = map[string]any{"$and": []any{left, right}}
	case filter.OpOr:
		v.condition = map[string]any{"$or": []any{left, right}}
	default:
		return fmt.Errorf("pinecone: unexpected logical operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
	}

	return nil
}

func (v *visitor) visitNotExpr(expr *filter.UnaryExpr) error {
	condition, err := v.buildNegatedExpr(expr.Right())
	if err != nil {
		return fmt.Errorf("pinecone: process NOT operand at %s: %w",
			expr.Start().String(), err)
	}
	v.condition = condition
	return nil
}

func (v *visitor) buildNegatedExpr(expr filter.Expr) (map[string]any, error) {
	switch node := expr.(type) {
	case *filter.UnaryExpr:
		if !node.Operator().Is(filter.OpNot) {
			return nil, fmt.Errorf("cannot negate unary operator %s", node.Operator().Name())
		}
		return v.buildNestedExpr(node.Right())
	case *filter.BinaryExpr:
		switch {
		case node.Operator().IsLogicalOperator():
			left, err := v.buildNegatedExpr(node.Left())
			if err != nil {
				return nil, err
			}
			right, err := v.buildNegatedExpr(node.Right())
			if err != nil {
				return nil, err
			}
			op := "$or"
			if node.Operator().Is(filter.OpOr) {
				op = "$and"
			}
			return map[string]any{op: []any{left, right}}, nil
		case node.Operator().IsComparisonOperator():
			inverted, err := node.Inverse()
			if err != nil {
				return nil, err
			}
			return v.buildNestedExpr(inverted)
		case node.Operator().Is(filter.OpIn):
			return v.buildListMembershipExpr(node, "$nin")
		case node.Operator().Is(filter.OpHas):
			return v.buildNegatedCollectionMembershipExpr(node)
		case node.Operator().IsNullOperator():
			// IS has no inverse operator to swap in, but its negation is
			// simply the other side of $exists.
			fieldKey, err := v.extractFieldKey(node.Left())
			if err != nil {
				return nil, fmt.Errorf("extract field key from 'IS NULL' at %s: %w",
					node.Start().String(), err)
			}
			return map[string]any{fieldKey: map[string]any{"$exists": true}}, nil
		default:
			return nil, fmt.Errorf("cannot negate operator %s", node.Operator().Name())
		}
	default:
		return nil, fmt.Errorf("cannot negate expression %T", expr)
	}
}

func (v *visitor) buildNegatedCollectionMembershipExpr(expr *filter.BinaryExpr) (map[string]any, error) {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return nil, err
	}
	fieldValue, err := v.extractFieldValue(expr.Right())
	if err != nil {
		return nil, err
	}
	if _, ok := fieldValue.(string); !ok {
		return nil, fmt.Errorf("HAS requires a string member at %s", expr.Start().String())
	}
	return map[string]any{fieldKey: map[string]any{"$ne": fieldValue}}, nil
}

func (v *visitor) visitEqualityExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("pinecone: extract field key from '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	fieldValue, err := v.extractFieldValue(expr.Right())
	if err != nil {
		return fmt.Errorf("pinecone: extract value from '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	switch expr.Operator() {
	case filter.OpEqual:
		v.condition = map[string]any{fieldKey: map[string]any{"$eq": fieldValue}}
	case filter.OpNotEqual:
		v.condition = map[string]any{fieldKey: map[string]any{"$ne": fieldValue}}
	default:
		return fmt.Errorf("pinecone: unexpected equality operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
	}

	return nil
}

func (v *visitor) visitOrderingExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("pinecone: extract field key from '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	fieldValue, err := v.extractFieldValue(expr.Right())
	if err != nil {
		return fmt.Errorf("pinecone: extract value from '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	switch expr.Operator() {
	case filter.OpLess:
		v.condition = map[string]any{fieldKey: map[string]any{"$lt": fieldValue}}
	case filter.OpLessEqual:
		v.condition = map[string]any{fieldKey: map[string]any{"$lte": fieldValue}}
	case filter.OpGreater:
		v.condition = map[string]any{fieldKey: map[string]any{"$gt": fieldValue}}
	case filter.OpGreaterEqual:
		v.condition = map[string]any{fieldKey: map[string]any{"$gte": fieldValue}}
	default:
		return fmt.Errorf("pinecone: unexpected ordering operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
	}

	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	result, err := v.buildListMembershipExpr(expr, "$in")
	if err != nil {
		return err
	}
	v.condition = result
	return nil
}

func (v *visitor) buildListMembershipExpr(expr *filter.BinaryExpr, operator string) (map[string]any, error) {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return nil, fmt.Errorf("pinecone: extract field key from '%s' at %s: %w",
			expr.Operator().Name(),
			expr.Start().String(), err)
	}

	listLit, err := expr.List()
	if err != nil {
		return nil, fmt.Errorf("pinecone: %w", err)
	}

	if err = v.visitListLiteral(listLit); err != nil {
		return nil, err
	}

	return map[string]any{fieldKey: map[string]any{operator: v.currentFieldValue}}, nil
}

func (v *visitor) buildNestedExpr(expr filter.Expr) (map[string]any, error) {
	nested := newVisitor()
	if err := nested.visit(expr); err != nil {
		return nil, err
	}
	if nested.condition != nil {
		return nested.condition, nil
	}
	return nil, fmt.Errorf("pinecone: unsupported expression type %T for nested expression", expr)
}

func (v *visitor) extractFieldKey(expr filter.Expr) (string, error) {
	savedKey := v.currentFieldKey
	v.currentFieldKey = ""

	err := v.visit(expr)

	extracted := v.currentFieldKey
	v.currentFieldKey = savedKey

	if err != nil {
		return "", err
	}
	if extracted == "" {
		return "", fmt.Errorf("pinecone: extract field key from %T expression", expr)
	}

	return extracted, nil
}

func (v *visitor) extractFieldValue(expr filter.Expr) (any, error) {
	savedValue := v.currentFieldValue
	v.currentFieldValue = nil

	err := v.visit(expr)

	extracted := v.currentFieldValue
	v.currentFieldValue = savedValue

	if err != nil {
		return nil, err
	}
	if extracted == nil {
		return nil, fmt.Errorf("pinecone: extract value from %T expression", expr)
	}

	return extracted, nil
}

func (v *visitor) buildIndexedFieldKey(expr *filter.IndexExpr) (string, error) {
	var parts []string

	current := expr
	for {
		key, err := current.Index().Key()
		if err != nil {
			return "", fmt.Errorf("pinecone: %w", err)
		}
		parts = append([]string{key}, parts...)

		switch left := current.Left().(type) {
		case *filter.IndexExpr:
			current = left
		case *filter.Ident:
			parts = append([]string{left.Name()}, parts...)
			return strings.Join(parts, "."), nil
		default:
			return "", fmt.Errorf("pinecone: invalid left operand type %T in index expression, expected identifier or index",
				left)
		}
	}
}

func (v *visitor) literalToValue(lit *filter.Literal) (any, error) {
	if lit.IsString() {
		return lit.AsString()
	}
	if lit.IsNumber() {
		return lit.Float64()
	}
	if lit.IsBool() {
		return lit.AsBool()
	}
	return nil, fmt.Errorf("pinecone: unsupported literal type '%s'", lit.Kind())
}
