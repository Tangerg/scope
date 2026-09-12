package qdrant

import (
	"fmt"
	"math"
	"strings"

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

func (v *visitor) visitEqualityExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("extract field key from left operand of '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	fieldValue, err := v.extractFieldValue(expr.Right())
	if err != nil {
		return fmt.Errorf("extract value from right operand of '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	matchCond, err := v.buildMatchCondition(fieldKey, fieldValue)
	if err != nil {
		return fmt.Errorf("create match condition for '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	switch expr.Operator() {
	case filter.OpEqual:
		v.filter.Must = append(v.filter.Must, matchCond)
	case filter.OpNotEqual:
		v.filter.MustNot = append(v.filter.MustNot, matchCond)
	default:
		return fmt.Errorf("unexpected equality operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
	}

	return nil
}

func (v *visitor) visitHasExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("extract collection field at %s: %w", expr.Start().String(), err)
	}
	fieldValue, err := v.extractFieldValue(expr.Right())
	if err != nil {
		return fmt.Errorf("extract collection member at %s: %w", expr.Start().String(), err)
	}
	matchCondition, err := v.buildMatchCondition(fieldKey, fieldValue)
	if err != nil {
		return fmt.Errorf("create collection membership condition at %s: %w", expr.Start().String(), err)
	}
	v.filter.Must = append(v.filter.Must, matchCondition)
	return nil
}

func (v *visitor) buildMatchCondition(fieldKey string, fieldValue any) (*qdrant.Condition, error) {
	switch v := fieldValue.(type) {
	case string:
		return qdrant.NewMatchKeyword(fieldKey, v), nil
	case int64:
		return qdrant.NewMatchInt(fieldKey, v), nil
	case uint64:
		if v > math.MaxInt64 {
			return nil, fmt.Errorf("integer %d exceeds Qdrant's int64 match range", v)
		}
		return qdrant.NewMatchInt(fieldKey, int64(v)), nil
	case float64:
		return nil, fmt.Errorf("qdrant match requires an integer, got %v", v)
	case bool:
		return qdrant.NewMatchBool(fieldKey, v), nil
	default:
		return nil, fmt.Errorf("unsupported value type %T for match condition", fieldValue)
	}
}

func (v *visitor) visitOrderingExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("extract field key from left operand of '%s' at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	literal, ok := expr.Right().(*filter.Literal)
	if !ok {
		return fmt.Errorf("right operand of '%s' at %s must be a number literal, got %T",
			expr.Operator().String(), expr.Start().String(), expr.Right())
	}
	numericValue, err := literal.Float64()
	if err != nil {
		return fmt.Errorf("cannot convert value for '%s' comparison at %s: %w",
			expr.Operator().String(), expr.Start().String(), err)
	}

	switch expr.Operator() {
	case filter.OpLess:
		v.filter.Must = append(v.filter.Must, qdrant.NewRange(fieldKey, &qdrant.Range{
			Lt: &numericValue,
		}))
	case filter.OpLessEqual:
		v.filter.Must = append(v.filter.Must, qdrant.NewRange(fieldKey, &qdrant.Range{
			Lte: &numericValue,
		}))
	case filter.OpGreater:
		v.filter.Must = append(v.filter.Must, qdrant.NewRange(fieldKey, &qdrant.Range{
			Gt: &numericValue,
		}))
	case filter.OpGreaterEqual:
		v.filter.Must = append(v.filter.Must, qdrant.NewRange(fieldKey, &qdrant.Range{
			Gte: &numericValue,
		}))
	default:
		return fmt.Errorf("unexpected ordering operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
	}

	return nil
}

func (v *visitor) visitInExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("extract field key from left operand of 'IN' at %s: %w",
			expr.Start().String(), err)
	}

	listLit, err := expr.List()
	if err != nil {
		return fmt.Errorf("qdrant: %w", err)
	}
	first, err := listLit.First()
	if err != nil {
		return fmt.Errorf("qdrant: IN values: %w", err)
	}

	switch {
	case first.IsString():
		keywords := make([]string, 0, listLit.Len())
		for _, literal := range listLit.Literals() {
			value, err := literal.AsString()
			if err != nil {
				return err
			}
			keywords = append(keywords, value)
		}
		v.filter.Must = append(v.filter.Must, qdrant.NewMatchKeywords(fieldKey, keywords...))

	case first.IsNumber():
		integers := make([]int64, 0, listLit.Len())
		for _, literal := range listLit.Literals() {
			value, err := literal.Int64()
			if err != nil {
				return fmt.Errorf("qdrant: IN numeric value: %w", err)
			}
			integers = append(integers, value)
		}
		v.filter.Must = append(v.filter.Must, qdrant.NewMatchInts(fieldKey, integers...))

	case first.IsBool():
		// Qdrant has no boolean-list matcher; nesting Should keeps this IN
		// expression's OR local instead of widening the enclosing filter.
		boolConditions := make([]*qdrant.Condition, 0, listLit.Len())
		for _, literal := range listLit.Literals() {
			value, err := literal.AsBool()
			if err != nil {
				return err
			}
			boolConditions = append(boolConditions, qdrant.NewMatchBool(fieldKey, value))
		}
		v.filter.Must = append(v.filter.Must,
			qdrant.NewFilterAsCondition(&qdrant.Filter{
				Should: boolConditions,
			}))

	default:
		return fmt.Errorf("unsupported literal kind %s in 'IN' list at %s",
			first.Kind(), expr.Start().String())
	}

	return nil
}

func (v *visitor) visitLikeExpr(expr *filter.BinaryExpr) error {
	fieldKey, err := v.extractFieldKey(expr.Left())
	if err != nil {
		return fmt.Errorf("qdrant: extract field key from left operand of LIKE at %s: %w",
			expr.Start().String(), err)
	}

	lit, ok := expr.Right().(*filter.Literal)
	if !ok {
		return fmt.Errorf("qdrant: LIKE requires a string literal on the right side at %s, got %T",
			expr.Start().String(), expr.Right())
	}

	if !lit.IsString() {
		return fmt.Errorf("qdrant: LIKE requires a string pattern at %s, got %s",
			expr.Start().String(), lit.Kind())
	}
	pattern, err := lit.AsString()
	if err != nil {
		return fmt.Errorf("qdrant: read LIKE pattern at %s: %w", expr.Start().String(), err)
	}
	if strings.ContainsAny(pattern, "%_") {
		return fmt.Errorf("qdrant: LIKE pattern %q cannot be represented exactly by Qdrant filters", pattern)
	}

	v.filter.Must = append(v.filter.Must, qdrant.NewMatchKeyword(fieldKey, pattern))
	return nil
}

func (v *visitor) buildNestedCondition(expr filter.Expr) (*qdrant.Condition, error) {
	switch node := expr.(type) {
	case *filter.BinaryExpr,
		*filter.UnaryExpr:
		nestedConv := newVisitor()
		err := nestedConv.visit(node)
		if err != nil {
			return nil, err
		}
		return qdrant.NewFilterAsCondition(nestedConv.filter), nil

	default:
		return nil, fmt.Errorf("unsupported expression type %T for condition building", node)
	}
}

func (v *visitor) extractFieldKey(expr filter.Expr) (string, error) {
	savedFieldKey := v.currentFieldKey
	v.currentFieldKey = ""

	err := v.visit(expr)

	extractedKey := v.currentFieldKey
	v.currentFieldKey = savedFieldKey

	if err != nil {
		return "", err
	}

	if extractedKey == "" {
		return "", fmt.Errorf("extract field key from %T expression", expr)
	}

	return extractedKey, nil
}

func (v *visitor) extractFieldValue(expr filter.Expr) (any, error) {
	savedFieldValue := v.currentFieldValue
	v.currentFieldValue = nil

	err := v.visit(expr)

	extractedValue := v.currentFieldValue
	v.currentFieldValue = savedFieldValue

	if err != nil {
		return nil, err
	}

	if extractedValue == nil {
		return nil, fmt.Errorf("extract value from %T expression", expr)
	}

	return extractedValue, nil
}

func (v *visitor) buildIndexedFieldKey(expr *filter.IndexExpr) (string, error) {
	var pathParts []string

	currentExpr := expr
	for {
		key, err := currentExpr.Index().Key()
		if err != nil {
			return "", err
		}
		pathParts = append([]string{key}, pathParts...)

		switch leftNode := currentExpr.Left().(type) {
		case *filter.IndexExpr:
			currentExpr = leftNode
		case *filter.Ident:
			pathParts = append([]string{leftNode.Name()}, pathParts...)
			return strings.Join(pathParts, "."), nil
		default:
			return "", fmt.Errorf("invalid left operand type %T in index expression, expected identifier or index", leftNode)
		}
	}
}

func (v *visitor) literalToValue(lit *filter.Literal) (any, error) {
	if lit.IsString() {
		return lit.AsString()
	}

	if lit.IsNumber() {
		return lit.Value()
	}

	if lit.IsBool() {
		return lit.AsBool()
	}

	return nil, fmt.Errorf("unsupported literal type '%s'", lit.Kind())
}
