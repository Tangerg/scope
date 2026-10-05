package milvus

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

type compiledPredicate struct {
	condition string
	invalid   string
}

type visitor struct{ result compiledPredicate }

func newVisitor() *visitor { return &visitor{} }

func (v *visitor) snapshot() string {
	if v.result.invalid == "" {
		return v.result.condition
	}
	return "(" + v.result.condition + ") and not (" + v.result.invalid + ")"
}

func (v *visitor) invalid() string { return v.result.invalid }

func (v *visitor) Visit(predicate filter.Predicate) error {
	v.result = compiledPredicate{}
	result, err := v.compilePredicate(predicate)
	if err != nil {
		return err
	}
	v.result = result
	return nil
}

func (v *visitor) compilePredicate(predicate filter.Predicate) (compiledPredicate, error) {
	switch expression := predicate.(type) {
	case *filter.BinaryExpr:
		return v.compileBinary(expression)
	case *filter.UnaryExpr:
		operand, err := v.compilePredicate(expression.Right())
		if err != nil {
			return compiledPredicate{}, err
		}
		return compiledPredicate{condition: "not (" + operand.condition + ")", invalid: operand.invalid}, nil
	default:
		return compiledPredicate{}, fmt.Errorf("milvus: unsupported predicate %T", predicate)
	}
}

func (v *visitor) compileBinary(expression *filter.BinaryExpr) (compiledPredicate, error) {
	operator := expression.Operator()
	if operator.IsLogicalOperator() {
		return v.compileLogical(expression)
	}
	if operator == filter.OpNotEqual {
		equality, err := expression.Inverse()
		if err != nil {
			return compiledPredicate{}, err
		}
		return v.compilePredicate(filter.Not(equality))
	}
	path, err := projectedPath(expression)
	if err != nil {
		return compiledPredicate{}, err
	}
	if operator.IsNullOperator() {
		return compiledPredicate{condition: "not (" + projectionPresence(fieldMetadataFilter+`["present"]`, path) + ")"}, nil
	}
	var condition string
	var kind metadataKind
	switch {
	case operator.IsComparisonOperator():
		condition, kind, err = v.compileComparison(expression, path)
	case operator == filter.OpIn:
		condition, kind, err = v.compileIn(expression, path)
	case operator == filter.OpHas:
		condition, kind, err = v.compileHas(expression, path)
	case operator == filter.OpLike:
		condition, kind, err = v.compileLike(expression, path)
	default:
		return compiledPredicate{}, fmt.Errorf("milvus: unsupported operator %s", operator)
	}
	if err != nil {
		return compiledPredicate{}, err
	}
	// Every atom becomes a definite bool before composition. The guard comes
	// from metadata, not native JSON's UNKNOWN or coerced path interpretation.
	guard := projectionPresence(projectionValue("kinds", string(kind)), path)
	result := compiledPredicate{condition: "(" + guard + " and " + condition + ")"}
	if operator == filter.OpLike || operator.IsComparisonOperator() && operator != filter.OpEqual {
		result.invalid = "(" + projectionPresence(fieldMetadataFilter+`["present"]`, path) + " and not (" + guard + "))"
	}
	return result, nil
}

func (v *visitor) compileLogical(expression *filter.BinaryExpr) (compiledPredicate, error) {
	left, err := v.compileOperand(expression.Left())
	if err != nil {
		return compiledPredicate{}, err
	}
	right, err := v.compileOperand(expression.Right())
	if err != nil {
		return compiledPredicate{}, err
	}
	operator := "and"
	if expression.Operator() == filter.OpOr {
		operator = "or"
	}
	result := compiledPredicate{condition: fmt.Sprintf("(%s) %s (%s)", left.condition, operator, right.condition), invalid: left.invalid}
	// Core evaluates left first and may never evaluate right. Error selection
	// must preserve that ordering even when native boolean evaluation does not.
	if right.invalid != "" {
		gate := left.condition
		if expression.Operator() == filter.OpOr {
			gate = "not (" + gate + ")"
		}
		rightInvalid := "(" + gate + ") and (" + right.invalid + ")"
		if result.invalid == "" {
			result.invalid = rightInvalid
		} else {
			result.invalid = "(" + result.invalid + ") or (" + rightInvalid + ")"
		}
	}
	return result, nil
}

func (v *visitor) compileOperand(expression filter.Expr) (compiledPredicate, error) {
	predicate, ok := expression.(filter.Predicate)
	if !ok {
		return compiledPredicate{}, fmt.Errorf("milvus: expected predicate, got %T", expression)
	}
	return v.compilePredicate(predicate)
}

func (v *visitor) compileComparison(expression *filter.BinaryExpr, path string) (string, metadataKind, error) {
	literal, err := expression.Literal()
	if err != nil {
		return "", "", err
	}
	value, kind, err := projectedLiteral(literal)
	if err != nil {
		return "", "", err
	}
	operators := map[filter.Operator]string{
		filter.OpEqual: "==", filter.OpLess: "<", filter.OpLessEqual: "<=",
		filter.OpGreater: ">", filter.OpGreaterEqual: ">=",
	}
	operator, ok := operators[expression.Operator()]
	if !ok {
		return "", "", fmt.Errorf("milvus: unsupported comparison %s", expression.Operator())
	}
	return projectionValue("scalars", path) + " " + operator + " " + value, kind, nil
}

func (v *visitor) compileIn(expression *filter.BinaryExpr, path string) (string, metadataKind, error) {
	list, err := expression.List()
	if err != nil {
		return "", "", err
	}
	values := make([]string, 0, list.Len())
	var kind metadataKind
	for _, literal := range list.Literals() {
		value, valueKind, valueErr := projectedLiteral(literal)
		if valueErr != nil {
			return "", "", valueErr
		}
		values = append(values, value)
		kind = valueKind
	}
	return projectionValue("scalars", path) + " in [" + strings.Join(values, ", ") + "]", kind, nil
}

func (v *visitor) compileHas(expression *filter.BinaryExpr, path string) (string, metadataKind, error) {
	literal, err := expression.Literal()
	if err != nil {
		return "", "", err
	}
	value, _, err := projectedLiteral(literal)
	if err != nil {
		return "", "", err
	}
	return "ARRAY_CONTAINS(" + projectionValue("members", path) + ", " + value + ")", metadataArray, nil
}

func (v *visitor) compileLike(expression *filter.BinaryExpr, path string) (string, metadataKind, error) {
	pattern, err := expression.Pattern()
	if err != nil {
		return "", "", err
	}
	operator := "like"
	if !strings.ContainsAny(pattern, "%_") {
		operator = "=="
	}
	encoded := string(metadataString) + ":" + encodeMetadataString(pattern, true)
	return projectionValue("scalars", path) + " " + operator + " " + strconv.Quote(encoded), metadataString, nil
}

func projectionPresence(array, path string) string {
	return "ARRAY_CONTAINS(" + array + ", " + strconv.Quote(path) + ")"
}

func projectionValue(field, key string) string {
	return fieldMetadataFilter + `["` + field + `"][` + strconv.Quote(key) + "]"
}

func projectedLiteral(literal *filter.Literal) (string, metadataKind, error) {
	var value any
	var err error
	switch {
	case literal.IsString():
		value, err = literal.AsString()
	case literal.IsNumber():
		value, err = literal.AsNumber()
	case literal.IsBool():
		value, err = literal.AsBool()
	default:
		return "", "", fmt.Errorf("milvus: unsupported literal %s", literal.Kind())
	}
	if err != nil {
		return "", "", err
	}
	encoded, kind, _, err := encodeMetadataScalar(value)
	return strconv.Quote(encoded), kind, err
}
