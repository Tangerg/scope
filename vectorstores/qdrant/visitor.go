package qdrant

import (
	"fmt"

	"github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor compiles Scope filter expressions into Qdrant conditions. A value is
// reusable: Visit replaces the previous result. Nested boolean operands compile
// in isolated visitors because Qdrant represents AND, OR, and NOT in separate
// condition lists; sharing temporary extraction state would change grouping.
// Unsupported or lossy provider mappings fail instead of approximating the
// source predicate.
type visitor struct {
	err               error
	filter            *qdrant.Filter
	currentFieldValue any
	currentFieldKey   string
}

func newVisitor() *visitor {
	return &visitor{
		filter: &qdrant.Filter{},
	}
}

// Failed compilation clears the prior value so a reused compiler cannot leak a stale filter.
func (v *visitor) snapshot() *qdrant.Filter {
	if v.err != nil {
		return nil
	}
	return v.filter
}

// Visit replaces prior state and accepts only trees Qdrant can represent
// without changing their meaning.
func (v *visitor) Visit(expr filter.Predicate) error {
	v.filter = &qdrant.Filter{}
	v.currentFieldValue = nil
	v.currentFieldKey = ""
	v.err = v.visit(expr)
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
		return fmt.Errorf("unsupported expression type %T", node)
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

func (v *visitor) visitComparisonExpr(expr *filter.BinaryExpr) error {
	if expr.Operator().IsEqualityOperator() {
		return v.visitEqualityExpr(expr)
	}
	return v.visitOrderingExpr(expr)
}

// visitNullTestExpr emits is_empty, not is_null.
//
// Qdrant separates the two: is_null "will match all records where the field
// exists and has NULL value", while is_empty matches records where the field
// "either does not exist, or has null or [] value". The filter AST treats an
// absent key and an explicit null as the same value, and an absent key is the
// ordinary case for metadata, so is_null would answer nothing for the
// documents the test is usually asked about.
//
// is_empty is the closest condition Qdrant offers and is wider in one respect:
// it also matches a key holding an empty array, which the AST reports as
// non-null. Qdrant has no condition that separates that case, so the
// difference is stated rather than papered over.
func (v *visitor) visitNullTestExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("extract field key from left operand of 'IS NULL' at %s: %w",
			expr.Start().String(), err)
	}

	v.filter.Must = append(v.filter.Must, qdrant.NewIsEmpty(fieldKey))
	return nil
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
		return fmt.Errorf("convert literal at %s: %w",
			lit.Start().String(), err)
	}
	v.currentFieldValue = value
	return nil
}

func (v *visitor) visitListLiteral(list *filter.ListLiteral) error {
	values := make([]any, 0, list.Len())
	for i, lit := range list.Literals() {
		value, err := v.literalToValue(lit)
		if err != nil {
			return fmt.Errorf("convert list element at index %d: %w", i, err)
		}
		values = append(values, value)
	}
	v.currentFieldValue = values
	return nil
}

func (v *visitor) visitIndexExpr(expr *filter.IndexExpr) error {
	fieldKey, err := v.buildIndexedFieldKey(expr)
	if err != nil {
		return fmt.Errorf("build field path at %s: %w",
			expr.Start().String(), err)
	}
	v.currentFieldKey = fieldKey
	return nil
}

func (v *visitor) visitLogicalExpr(expr *filter.BinaryExpr) error {
	leftCond, err := v.buildNestedCondition(expr.Left())
	if err != nil {
		return fmt.Errorf("process left operand of '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	rightCond, err := v.buildNestedCondition(expr.Right())
	if err != nil {
		return fmt.Errorf("process right operand of '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	switch expr.Operator() {
	case filter.OpAnd:
		v.filter.Must = append(v.filter.Must, leftCond, rightCond)
		return nil
	case filter.OpOr:
		v.filter.Should = append(v.filter.Should, leftCond, rightCond)
		return nil
	default:
		return fmt.Errorf("unexpected logical operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
	}
}

func (v *visitor) visitNotExpr(expr *filter.UnaryExpr) error {
	cond, err := v.buildNestedCondition(expr.Right())
	if err != nil {
		return fmt.Errorf("process NOT operand at %s: %w",
			expr.Start().String(), err)
	}

	v.filter.MustNot = append(v.filter.MustNot, cond)
	return nil
}
