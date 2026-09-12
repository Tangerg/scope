package weaviate

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/weaviate/weaviate-go-client/v5/weaviate/filters"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// visitor compiles Scope filter expressions into Weaviate where filters.
// A visitor can be reused; each call to Visit replaces the previous result.
type visitor struct {
	result   *filters.WhereBuilder
	declared map[string]struct{}
}

var _ filter.Visitor = (*visitor)(nil)

// newVisitor binds the metadata keys declared on the class. A Weaviate where
// filter may only name a declared property, so a filter on any other key is
// refused rather than compiled into a path the class has no field for.
func newVisitor(declared []MetadataProperty) *visitor {
	names := make(map[string]struct{}, len(declared))
	for _, property := range declared {
		names[property.Name] = struct{}{}
	}
	return &visitor{declared: names}
}

// Visit compiles the complete expression tree rooted at expr.
func (v *visitor) Visit(expr filter.Predicate) error {
	v.result = nil
	if err := v.checkDeclaredPaths(expr); err != nil {
		return err
	}
	result, err := v.compileFilter(expr)
	if err != nil {
		return err
	}
	v.result = result
	return nil
}

// checkDeclaredPaths refuses a filter that selects on a key the class does not
// declare.
//
// Weaviate classes are typed and a where filter may only name a declared
// property. Compiling an undeclared key produced a path with no matching
// field, which is not a narrower query — it is a query the server cannot
// answer. Checking the whole tree up front keeps the refusal a property of the
// filter rather than of whichever leaf the compiler happened to reach first.
func (v *visitor) checkDeclaredPaths(expr filter.Expr) error {
	switch node := expr.(type) {
	case *filter.UnaryExpr:
		return v.checkDeclaredPaths(node.Right())
	case *filter.BinaryExpr:
		if node.Operator().IsLogicalOperator() {
			if err := v.checkDeclaredPaths(node.Left()); err != nil {
				return err
			}
			return v.checkDeclaredPaths(node.Right())
		}
		path, err := node.Path()
		if err != nil {
			return fmt.Errorf("weaviate.filter: left operand at %s: %w", node.Start(), err)
		}
		if len(path) == 0 {
			return fmt.Errorf("weaviate.filter: empty key path at %s", node.Start())
		}
		if len(path) > 1 {
			return fmt.Errorf(
				"weaviate.filter: nested key %q at %s cannot be filtered; declare a flat MetadataProperty instead",
				strings.Join(path, "."), node.Start())
		}
		if _, ok := v.declared[path[0]]; !ok {
			return fmt.Errorf(
				"weaviate.filter: metadata key %q at %s is not declared in MetadataProperties, so the class has no property to filter on",
				path[0], node.Start())
		}
		return nil
	default:
		return nil
	}
}

// Failed compilation clears the prior value so a reused compiler cannot leak a stale filter.
func (v *visitor) snapshot() *filters.WhereBuilder {
	return v.result
}

func (v *visitor) compileFilter(expr filter.Expr) (*filters.WhereBuilder, error) {
	switch node := expr.(type) {
	case *filter.BinaryExpr:
		return v.compileBinary(node)
	case *filter.UnaryExpr:
		return v.compileUnary(node)
	default:
		return nil, fmt.Errorf("weaviate.filter: expected predicate, got %T", expr)
	}
}

func (v *visitor) compileBinary(expr *filter.BinaryExpr) (*filters.WhereBuilder, error) {
	switch {
	case expr.Operator().IsNullOperator():
		return v.compileNullTest(expr)
	case expr.Operator().IsLogicalOperator():
		return v.compileLogical(expr)
	case expr.Operator().IsComparisonOperator():
		return v.compileComparison(expr)
	case expr.Operator().Is(filter.OpIn):
		return v.compileIn(expr)
	case expr.Operator().Is(filter.OpHas):
		return v.compileHas(expr)
	case expr.Operator().Is(filter.OpLike):
		return v.compileLike(expr)
	default:
		return nil, fmt.Errorf("weaviate.filter: unsupported binary operator %q at %s",
			expr.Operator().String(), expr.Start())
	}
}

func (v *visitor) compileUnary(expr *filter.UnaryExpr) (*filters.WhereBuilder, error) {
	if !expr.Operator().Is(filter.OpNot) {
		return nil, fmt.Errorf("weaviate.filter: unsupported unary operator %q at %s",
			expr.Operator().String(), expr.Start())
	}
	operand, err := v.compileFilter(expr.Right())
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: NOT operand: %w", err)
	}
	return filters.Where().
		WithOperator(filters.Not).
		WithOperands([]*filters.WhereBuilder{operand}), nil
}

func (v *visitor) compileLogical(expr *filter.BinaryExpr) (*filters.WhereBuilder, error) {
	left, err := v.compileFilter(expr.Left())
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: left operand of %s: %w", expr.Operator(), err)
	}
	right, err := v.compileFilter(expr.Right())
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: right operand of %s: %w", expr.Operator(), err)
	}

	var operator filters.WhereOperator
	switch expr.Operator() {
	case filter.OpAnd:
		operator = filters.And
	case filter.OpOr:
		operator = filters.Or
	default:
		return nil, fmt.Errorf("weaviate.filter: unsupported logical operator %q", expr.Operator())
	}
	return filters.Where().
		WithOperator(operator).
		WithOperands([]*filters.WhereBuilder{left, right}), nil
}

func (v *visitor) compileComparison(expr *filter.BinaryExpr) (*filters.WhereBuilder, error) {
	path, err := expr.Path()
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: left operand of %s: %w", expr.Operator(), err)
	}
	literal, ok := expr.Right().(*filter.Literal)
	if !ok || literal == nil {
		return nil, fmt.Errorf("weaviate.filter: right operand of %s must be a literal, got %T at %s",
			expr.Operator(), expr.Right(), expr.Start())
	}

	operator, err := comparisonOperator(expr.Operator())
	if err != nil {
		return nil, err
	}
	if !expr.Operator().IsEqualityOperator() && !literal.IsNumber() {
		return nil, fmt.Errorf("weaviate.filter: %s requires a numeric right operand at %s",
			expr.Operator(), expr.Start())
	}
	return scalarFilter(path, operator, literal)
}

func (v *visitor) compileIn(expr *filter.BinaryExpr) (*filters.WhereBuilder, error) {
	path, err := expr.Path()
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: left operand of IN: %w", err)
	}
	list, err := expr.List()
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: %w", err)
	}
	if _, err := list.Values(); err != nil {
		return nil, fmt.Errorf("weaviate.filter: IN values: %w", err)
	}

	operands := make([]*filters.WhereBuilder, 0, list.Len())
	for _, literal := range list.Literals() {
		operand, err := scalarFilter(path, filters.Equal, literal)
		if err != nil {
			return nil, fmt.Errorf("weaviate.filter: IN value: %w", err)
		}
		operands = append(operands, operand)
	}
	return filters.Where().
		WithOperator(filters.Or).
		WithOperands(operands), nil
}

func (v *visitor) compileHas(expr *filter.BinaryExpr) (*filters.WhereBuilder, error) {
	path, err := expr.Path()
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: left operand of HAS: %w", err)
	}
	literal, ok := expr.Right().(*filter.Literal)
	if !ok || literal == nil {
		return nil, fmt.Errorf("weaviate.filter: right operand of HAS must be a literal, got %T at %s",
			expr.Right(), expr.Start())
	}
	return scalarFilter(path, filters.ContainsAny, literal)
}

func (v *visitor) compileLike(expr *filter.BinaryExpr) (*filters.WhereBuilder, error) {
	path, err := expr.Path()
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: left operand of LIKE: %w", err)
	}
	pattern, err := expr.Pattern()
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: %w", err)
	}
	translated, err := weaviateLikePattern(pattern)
	if err != nil {
		return nil, err
	}
	return filters.Where().
		WithPath(path).
		WithOperator(filters.Like).
		WithValueText(translated), nil
}

func (v *visitor) compileNullTest(expr *filter.BinaryExpr) (*filters.WhereBuilder, error) {
	path, err := expr.Path()
	if err != nil {
		return nil, fmt.Errorf("weaviate.filter: left operand of IS NULL: %w", err)
	}
	return filters.Where().
		WithPath(path).
		WithOperator(filters.IsNull).
		WithValueBoolean(true), nil
}

func comparisonOperator(operator filter.Operator) (filters.WhereOperator, error) {
	switch operator {
	case filter.OpEqual:
		return filters.Equal, nil
	case filter.OpNotEqual:
		return filters.NotEqual, nil
	case filter.OpLess:
		return filters.LessThan, nil
	case filter.OpLessEqual:
		return filters.LessThanEqual, nil
	case filter.OpGreater:
		return filters.GreaterThan, nil
	case filter.OpGreaterEqual:
		return filters.GreaterThanEqual, nil
	default:
		return "", fmt.Errorf("weaviate.filter: unsupported comparison operator %q", operator)
	}
}

func scalarFilter(
	path []string,
	operator filters.WhereOperator,
	literal *filter.Literal,
) (*filters.WhereBuilder, error) {
	builder := filters.Where().WithPath(path).WithOperator(operator)
	switch {
	case literal.IsString():
		value, err := literal.AsString()
		if err != nil {
			return nil, fmt.Errorf("weaviate.filter: string literal: %w", err)
		}
		return builder.WithValueText(value), nil
	case literal.IsBool():
		value, err := literal.AsBool()
		if err != nil {
			return nil, fmt.Errorf("weaviate.filter: boolean literal: %w", err)
		}
		return builder.WithValueBoolean(value), nil
	case literal.IsNumber():
		value, err := literal.Value()
		if err != nil {
			return nil, fmt.Errorf("weaviate.filter: number literal: %w", err)
		}
		switch number := value.(type) {
		case int64:
			return builder.WithValueInt(number), nil
		case uint64:
			if number > math.MaxInt64 {
				return nil, fmt.Errorf("weaviate.filter: integer %q exceeds Weaviate int64", literal.Text())
			}
			return builder.WithValueInt(int64(number)), nil
		case float64:
			return builder.WithValueNumber(number), nil
		default:
			return nil, fmt.Errorf("weaviate.filter: unsupported numeric value %T", value)
		}
	default:
		return nil, fmt.Errorf("weaviate.filter: unsupported literal kind %s", literal.Kind())
	}
}

// weaviateLikePattern translates SQL LIKE wildcards into Weaviate's wildcard
// syntax. Weaviate cannot escape literal '*' or '?', so accepting either
// would broaden the predicate and violate the source expression.
func weaviateLikePattern(pattern string) (string, error) {
	if strings.ContainsAny(pattern, "*?") {
		return "", errors.New("weaviate.filter: LIKE cannot represent literal '*' or '?' characters")
	}
	return strings.NewReplacer("%", "*", "_", "?").Replace(pattern), nil
}
