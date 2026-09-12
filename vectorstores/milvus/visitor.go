package milvus

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor compiles Scope filter expressions into Milvus's string expression
// language. A value is reusable: Visit resets the previous result before
// compiling the complete immutable tree. Numeric literals retain their
// canonical text instead of passing through float64.
type visitor struct {
	result string
}

func newVisitor() *visitor {
	return &visitor{}
}

// Failed compilation clears the prior value so a reused compiler cannot leak a stale filter.
func (v *visitor) snapshot() string {
	return v.result
}

// Visit replaces prior state and accepts only trees Milvus can represent
// without changing their meaning.
func (v *visitor) Visit(predicate filter.Predicate) error {
	v.result = ""
	result, err := v.compilePredicate(predicate)
	if err != nil {
		return err
	}
	v.result = result
	return nil
}

func (v *visitor) compilePredicate(predicate filter.Predicate) (string, error) {
	switch expression := predicate.(type) {
	case *filter.BinaryExpr:
		return v.compileBinary(expression)
	case *filter.UnaryExpr:
		return v.compileNot(expression)
	default:
		return "", fmt.Errorf("milvus: unsupported predicate type %T", expression)
	}
}

func (v *visitor) compileBinary(expression *filter.BinaryExpr) (string, error) {
	switch operator := expression.Operator(); {
	case operator.IsLogicalOperator():
		return v.compileLogical(expression)
	case operator.IsComparisonOperator():
		return v.compileComparison(expression)
	case operator.Is(filter.OpIn):
		return v.compileIn(expression)
	case operator.Is(filter.OpHas):
		return v.compileHas(expression)
	case operator.Is(filter.OpLike):
		return v.compileLike(expression)
	default:
		return "", fmt.Errorf("milvus: unsupported binary operator '%s' at %s", operator, expression.Start())
	}
}

func (v *visitor) compileLogical(expression *filter.BinaryExpr) (string, error) {
	left, err := v.compileOperand(expression.Left())
	if err != nil {
		return "", fmt.Errorf("milvus: process left operand of '%s' at %s: %w", expression.Operator(), expression.Start(), err)
	}
	right, err := v.compileOperand(expression.Right())
	if err != nil {
		return "", fmt.Errorf("milvus: process right operand of '%s' at %s: %w", expression.Operator(), expression.Start(), err)
	}
	operator, ok := map[filter.Operator]string{
		filter.OpAnd: "and",
		filter.OpOr:  "or",
	}[expression.Operator()]
	if !ok {
		return "", fmt.Errorf("milvus: unexpected logical operator '%s' at %s", expression.Operator(), expression.Start())
	}
	return fmt.Sprintf("(%s) %s (%s)", left, operator, right), nil
}

func (v *visitor) compileOperand(expression filter.Expr) (string, error) {
	predicate, ok := expression.(filter.Predicate)
	if !ok {
		return "", fmt.Errorf("milvus: expected predicate operand, got %T", expression)
	}
	return v.compilePredicate(predicate)
}

func (v *visitor) compileNot(expression *filter.UnaryExpr) (string, error) {
	if expression.Operator() != filter.OpNot {
		return "", fmt.Errorf("milvus: unexpected unary operator '%s' at %s", expression.Operator(), expression.Start())
	}
	operand, err := v.compilePredicate(expression.Right())
	if err != nil {
		return "", fmt.Errorf("milvus: process NOT operand at %s: %w", expression.Start(), err)
	}
	return fmt.Sprintf("not (%s)", operand), nil
}

func (v *visitor) compileComparison(expression *filter.BinaryExpr) (string, error) {
	fieldKey, err := selectorString(expression)
	if err != nil {
		return "", fmt.Errorf("milvus: extract field key from '%s' at %s: %w", expression.Operator(), expression.Start(), err)
	}
	literal, err := expression.Literal()
	if err != nil {
		return "", err
	}
	fieldValue, err := literalString(literal)
	if err != nil {
		return "", fmt.Errorf("milvus: extract value from '%s' at %s: %w", expression.Operator(), expression.Start(), err)
	}
	operator, ok := map[filter.Operator]string{
		filter.OpEqual:        "==",
		filter.OpNotEqual:     "!=",
		filter.OpLess:         "<",
		filter.OpLessEqual:    "<=",
		filter.OpGreater:      ">",
		filter.OpGreaterEqual: ">=",
	}[expression.Operator()]
	if !ok {
		return "", fmt.Errorf("milvus: unexpected comparison operator '%s' at %s", expression.Operator(), expression.Start())
	}
	return fmt.Sprintf("%s %s %s", fieldKey, operator, fieldValue), nil
}

func (v *visitor) compileIn(expression *filter.BinaryExpr) (string, error) {
	fieldKey, err := selectorString(expression)
	if err != nil {
		return "", fmt.Errorf("milvus: extract field key from 'IN' at %s: %w", expression.Start(), err)
	}
	list, err := expression.List()
	if err != nil {
		return "", fmt.Errorf("milvus: %w", err)
	}
	value, err := listString(list)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s in %s", fieldKey, value), nil
}

func (v *visitor) compileHas(expression *filter.BinaryExpr) (string, error) {
	fieldKey, err := selectorString(expression)
	if err != nil {
		return "", fmt.Errorf("milvus: extract collection field at %s: %w", expression.Start(), err)
	}
	literal, err := expression.Literal()
	if err != nil {
		return "", err
	}
	fieldValue, err := literalString(literal)
	if err != nil {
		return "", fmt.Errorf("milvus: extract collection member at %s: %w", expression.Start(), err)
	}
	return fmt.Sprintf("ARRAY_CONTAINS(%s, %s)", fieldKey, fieldValue), nil
}

func (v *visitor) compileLike(expression *filter.BinaryExpr) (string, error) {
	fieldKey, err := selectorString(expression)
	if err != nil {
		return "", fmt.Errorf("milvus: extract field key from 'LIKE' at %s: %w", expression.Start(), err)
	}
	literal, err := expression.Literal()
	if err != nil {
		return "", err
	}
	if !literal.IsString() {
		return "", fmt.Errorf("milvus: 'LIKE' operator requires a string pattern at %s, got %s", expression.Start(), literal.Kind())
	}
	pattern, err := literalString(literal)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s like %s", fieldKey, pattern), nil
}

func selectorString(expression *filter.BinaryExpr) (string, error) {
	selector, err := expression.Selector()
	if err != nil {
		return "", err
	}
	return formatSelector(selector)
}

func formatSelector(selector filter.Selector) (string, error) {
	switch expression := selector.(type) {
	case *filter.Ident:
		return expression.Name(), nil
	case *filter.IndexExpr:
		left, err := formatSelector(expression.Left())
		if err != nil {
			return "", err
		}
		key, err := expression.Index().Key()
		if err != nil {
			return "", fmt.Errorf("milvus: %w", err)
		}
		if expression.Index().IsString() {
			key = strconv.Quote(key)
		}
		return left + "[" + key + "]", nil
	default:
		return "", fmt.Errorf("milvus: unsupported selector type %T", expression)
	}
}

func listString(list *filter.ListLiteral) (string, error) {
	parts := make([]string, 0, list.Len())
	for index, literal := range list.Literals() {
		value, err := literalString(literal)
		if err != nil {
			return "", fmt.Errorf("milvus: convert list element at index %d: %w", index, err)
		}
		parts = append(parts, value)
	}
	return "[" + strings.Join(parts, ", ") + "]", nil
}

func literalString(literal *filter.Literal) (string, error) {
	if literal.IsString() {
		value, err := literal.AsString()
		if err != nil {
			return "", fmt.Errorf("milvus: convert string literal at %s: %w", literal.Start(), err)
		}
		return strconv.Quote(value), nil
	}
	if literal.IsNumber() {
		value, err := literal.NumberText()
		if err != nil {
			return "", fmt.Errorf("milvus: convert number literal at %s: %w", literal.Start(), err)
		}
		return value, nil
	}
	if literal.IsBool() {
		value, err := literal.AsBool()
		if err != nil {
			return "", fmt.Errorf("milvus: convert bool literal at %s: %w", literal.Start(), err)
		}
		if value {
			return "True", nil
		}
		return "False", nil
	}
	return "", fmt.Errorf("milvus: unsupported literal type '%s' at %s", literal.Kind(), literal.Start())
}
