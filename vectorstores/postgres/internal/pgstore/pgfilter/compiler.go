package pgfilter

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*Compiler)(nil)

// Query projects Core filter truth into native JSONB predicates. Invalid selects
// metadata whose evaluation must fail; Predicate excludes those rows even under
// NOT. Both expressions use Args, including its explicit parameter types.
type Query struct {
	Predicate string
	Invalid   string
	Args      []any
}

type compiledPredicate struct {
	condition string
	invalid   string
}

// Compiler preserves JSON types and binds both path keys and literal values.
// The metadata column is an SQL identifier validated by the store constructor.
type Compiler struct {
	metadataCol string
	result      compiledPredicate
	args        []any
}

func NewCompiler(metadataCol string) *Compiler {
	return &Compiler{metadataCol: metadataCol}
}

func (c *Compiler) Result() Query {
	condition := c.result.condition
	if c.result.invalid != "" {
		condition = "(" + condition + " AND NOT " + c.result.invalid + ")"
	}
	return Query{Predicate: condition, Invalid: c.result.invalid, Args: slices.Clone(c.args)}
}

func (c *Compiler) Visit(predicate filter.Predicate) error {
	c.result = compiledPredicate{}
	c.args = nil
	result, err := c.compile(predicate)
	if err != nil {
		c.args = nil
		return err
	}
	c.result = result
	return nil
}

func (c *Compiler) compile(expression filter.Expr) (compiledPredicate, error) {
	switch node := expression.(type) {
	case *filter.UnaryExpr:
		operand, err := c.compile(node.Right())
		if err != nil {
			return compiledPredicate{}, err
		}
		return compiledPredicate{condition: "(NOT " + operand.condition + ")", invalid: operand.invalid}, nil
	case *filter.BinaryExpr:
		if node.Operator().IsLogicalOperator() {
			return c.compileLogical(node)
		}
		return c.compileAtom(node)
	default:
		return compiledPredicate{}, fmt.Errorf("postgres: unsupported predicate %T", expression)
	}
}

func (c *Compiler) compileLogical(expression *filter.BinaryExpr) (compiledPredicate, error) {
	left, err := c.compile(expression.Left())
	if err != nil {
		return compiledPredicate{}, err
	}
	right, err := c.compile(expression.Right())
	if err != nil {
		return compiledPredicate{}, err
	}
	op, err := expression.Operator().LogicalString()
	if err != nil {
		return compiledPredicate{}, err
	}
	result := compiledPredicate{condition: "(" + left.condition + " " + op + " " + right.condition + ")", invalid: left.invalid}
	// SQL may reorder boolean evaluation. Error selection must still follow
	// Core's left-to-right short circuit, rather than every reachable atom.
	if right.invalid != "" {
		gate := left.condition
		if expression.Operator() == filter.OpOr {
			gate = "(NOT " + gate + ")"
		}
		rightInvalid := "(" + gate + " AND " + right.invalid + ")"
		if result.invalid == "" {
			result.invalid = rightInvalid
		} else {
			result.invalid = "(" + result.invalid + " OR " + rightInvalid + ")"
		}
	}
	return result, nil
}

func (c *Compiler) compileAtom(expression *filter.BinaryExpr) (compiledPredicate, error) {
	path, err := c.buildJSONPath(expression)
	if err != nil {
		return compiledPredicate{}, err
	}
	op := expression.Operator()
	if op.IsNullOperator() {
		return compiledPredicate{condition: "COALESCE(" + path + " = 'null'::jsonb, TRUE)"}, nil
	}
	if op == filter.OpIn {
		return c.compileIn(expression, path)
	}
	if op == filter.OpLike {
		pattern, patternErr := expression.Pattern()
		if patternErr != nil {
			return compiledPredicate{}, patternErr
		}
		if strings.ContainsRune(pattern, 0) {
			return compiledPredicate{}, errors.New("postgres: LIKE pattern contains a NUL unsupported by PostgreSQL text")
		}
		bound := c.bind(pattern, "text")
		return compiledPredicate{
			condition: "COALESCE((jsonb_typeof(" + path + ") = 'string' AND (" + path + " #>> '{}') LIKE " + bound + " ESCAPE ''), FALSE)",
			invalid:   c.mismatchedType(path, "string"),
		}, nil
	}
	value, err := expression.Value()
	if err != nil {
		return compiledPredicate{}, err
	}
	bound, err := c.bindLiteral(value)
	if err != nil {
		return compiledPredicate{}, err
	}
	if op == filter.OpHas {
		return compiledPredicate{condition: "COALESCE(" + path + " @> jsonb_build_array(" + bound + "), FALSE)"}, nil
	}
	if op == filter.OpEqual || op == filter.OpNotEqual {
		condition := "COALESCE(" + path + " = " + bound + ", FALSE)"
		if op == filter.OpNotEqual {
			condition = "(NOT " + condition + ")"
		}
		return compiledPredicate{condition: condition}, nil
	}
	operators := map[filter.Operator]string{
		filter.OpLess: "<", filter.OpLessEqual: "<=", filter.OpGreater: ">", filter.OpGreaterEqual: ">=",
	}
	operator, ok := operators[op]
	if !ok {
		return compiledPredicate{}, fmt.Errorf("postgres: unsupported comparison %s", op)
	}
	return compiledPredicate{
		condition: "COALESCE((jsonb_typeof(" + path + ") = 'number' AND " + path + " " + operator + " " + bound + "), FALSE)",
		invalid:   c.mismatchedType(path, "number"),
	}, nil
}

func (c *Compiler) compileIn(expression *filter.BinaryExpr, path string) (compiledPredicate, error) {
	list, err := expression.List()
	if err != nil {
		return compiledPredicate{}, err
	}
	values := make([]string, 0, list.Len())
	for _, literal := range list.Literals() {
		value, err := literal.Value()
		if err != nil {
			return compiledPredicate{}, err
		}
		bound, err := c.bindLiteral(value)
		if err != nil {
			return compiledPredicate{}, err
		}
		values = append(values, bound)
	}
	return compiledPredicate{condition: "COALESCE(" + path + " IN (" + strings.Join(values, ", ") + "), FALSE)"}, nil
}

func (c *Compiler) mismatchedType(path, kind string) string {
	return "COALESCE(jsonb_typeof(" + path + ") NOT IN ('" + kind + "', 'null'), FALSE)"
}

func (c *Compiler) bind(value any, sqlType string) string {
	c.args = append(c.args, value)
	return "$" + strconv.Itoa(len(c.args)) + "::" + sqlType
}

func (c *Compiler) bindLiteral(value any) (string, error) {
	if text, ok := value.(string); ok && strings.ContainsRune(text, 0) {
		return "", errors.New("postgres: literal contains a NUL unsupported by JSONB")
	}
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return "", err
	}
	return c.bind(string(encoded), "jsonb"), nil
}

func (c *Compiler) buildJSONPath(expression *filter.BinaryExpr) (string, error) {
	path, err := expression.Path()
	if err != nil {
		return "", err
	}
	selected := c.metadataCol
	var guards []string
	for _, segment := range path {
		if index, ok := segment.Index(); ok {
			if index > math.MaxInt32 {
				return "", fmt.Errorf("postgres: array index %d exceeds the jsonb integer operand range", index)
			}
			guards = append(guards, "jsonb_typeof("+selected+") = 'array'")
			selected = "(" + selected + " -> " + strconv.FormatUint(index, 10) + ")"
			continue
		}
		key, _ := segment.Key()
		if strings.ContainsRune(key, 0) {
			return "", errors.New("postgres: path key contains a NUL unsupported by PostgreSQL text")
		}
		selected = "(" + selected + " -> " + c.bind(key, "text") + ")"
	}
	// JSONB treats a scalar as element zero. Guard raw prefixes together so
	// nested indices neither read scalars nor duplicate guarded subtrees.
	if len(guards) > 0 {
		selected = "(CASE WHEN " + strings.Join(guards, " AND ") + " THEN " + selected + " END)"
	}
	return selected, nil
}
