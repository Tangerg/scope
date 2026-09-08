package mongodb

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

var _ filter.Visitor = (*visitor)(nil)

// visitor transforms AST filter expressions into the MongoDB query
// document shape used by Atlas Vector Search's `filter` clause.
//
// Output shape (MongoDB query language, keyed on metadata.* paths):
//
//	author == "Alice"             →  {"metadata.author": {"$eq": "Alice"}}
//	year >= 2020                  →  {"metadata.year":   {"$gte": 2020}}
//	category IN ("a", "b")        →  {"metadata.category": {"$in": ["a","b"]}}
//	NOT (year >= 2020)            →  {"$nor": [{"metadata.year": {"$gte": 2020}}]}
//	a == "x" AND b == "y"         →  {"$and": [{"metadata.a": {"$eq":"x"}},
//	                                            {"metadata.b": {"$eq":"y"}}]}
//	a == "x" OR b == "y"          →  {"$or":  […]}
//
// Identifier paths:
//   - bare identifier      → <metadataPrefix>.<ident>
//   - metadata['k']        → <metadataPrefix>.k
//   - metadata['a']['b']   → <metadataPrefix>.a.b
type visitor struct {
	err            error
	result         map[string]any
	metadataPrefix string // typically "metadata"
}

func newVisitor(metadataPrefix string) *visitor {
	return &visitor{metadataPrefix: metadataPrefix}
}

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

// translate is the recursive worker — it builds and returns the
// MongoDB sub-document for one expression, leaving the receiver state
// untouched. This avoids the stateful "currentField" shuffle the other
// visitors need.
func (v *visitor) translate(expr filter.Expr) (map[string]any, error) {
	if expr == nil {
		return nil, errors.New("mongodb: cannot process nil expression")
	}

	switch node := expr.(type) {
	case *filter.BinaryExpr:
		return v.translateBinary(node)
	case *filter.UnaryExpr:
		return v.translateUnary(node)
	default:
		return nil, fmt.Errorf("mongodb: unsupported root expression %T at %s",
			node, expr.Start().String())
	}
}

func (v *visitor) translateBinary(expr *filter.BinaryExpr) (map[string]any, error) {
	switch {
	case expr.Operator().IsNullOperator():
		return v.translateNullTest(expr)
	case expr.Operator().IsLogicalOperator():
		return v.translateLogical(expr)
	case expr.Operator().Is(filter.OpIn):
		return v.translateIn(expr, "$in")
	case expr.Operator().Is(filter.OpHas):
		return v.translateHas(expr)
	case expr.Operator().Is(filter.OpLike):
		return v.translateLike(expr)
	case expr.Operator().IsEqualityOperator() || expr.Operator().IsOrderingOperator():
		return v.translateComparison(expr)
	default:
		return nil, fmt.Errorf("mongodb: unsupported binary operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
	}
}

// translateHas uses MongoDB equality semantics, which match a scalar against
// any equal element when the selected field contains an array.
func (v *visitor) translateHas(expr *filter.BinaryExpr) (map[string]any, error) {
	field, err := v.fieldPath(expr)
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w (at %s)", err, expr.Start().String())
	}
	return map[string]any{field: map[string]any{"$eq": value}}, nil
}

func (v *visitor) translateUnary(expr *filter.UnaryExpr) (map[string]any, error) {
	if !expr.Operator().Is(filter.OpNot) {
		return nil, fmt.Errorf("mongodb: unsupported unary operator '%s' at %s",
			expr.Operator().String(), expr.Start().String())
	}
	inner, err := v.translate(expr.Right())
	if err != nil {
		return nil, err
	}
	// MongoDB has no top-level $not — $nor over a single-element array
	// is the idiomatic equivalent for "match documents that do NOT
	// satisfy this sub-expression".
	return map[string]any{"$nor": []any{inner}}, nil
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
	field, err := v.fieldPath(expr)
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w (at %s)", err, expr.Start().String())
	}
	value, err := expr.Value()
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w (at %s)", err, expr.Start().String())
	}
	op, err := mongoOpFor(expr.Operator())
	if err != nil {
		return nil, err
	}
	return map[string]any{field: map[string]any{op: value}}, nil
}

// translateNullTest emits `{field: {"$eq": null}}`. In MongoDB this
// matches documents where the field is explicitly null OR absent,
// which is the correct IS NULL semantics (parity with the inmemory
// reference). The negated `IS NOT NULL` arrives as NOT(field IS NULL)
// and is wrapped by translateUnary's $nor, so no separate handling is
// needed here.
func (v *visitor) translateNullTest(expr *filter.BinaryExpr) (map[string]any, error) {
	field, err := v.fieldPath(expr)
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w (at %s)", err, expr.Start().String())
	}
	return map[string]any{field: map[string]any{"$eq": nil}}, nil
}

func (v *visitor) translateIn(expr *filter.BinaryExpr, op string) (map[string]any, error) {
	field, err := v.fieldPath(expr)
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w (at %s)", err, expr.Start().String())
	}

	listLit, ok := expr.Right().(*filter.ListLiteral)
	if !ok {
		return nil, fmt.Errorf("mongodb: 'IN' requires a list on the right at %s, got %T",
			expr.Start().String(), expr.Right())
	}
	if listLit.Len() == 0 {
		return nil, fmt.Errorf("mongodb: 'IN' requires a non-empty list at %s",
			expr.Start().String())
	}

	values := make([]any, 0, listLit.Len())
	for _, lit := range listLit.Literals() {
		val, err := lit.Value()
		if err != nil {
			return nil, fmt.Errorf("mongodb: %w (at %s)", err, expr.Start().String())
		}
		values = append(values, val)
	}

	return map[string]any{field: map[string]any{op: values}}, nil
}

// translateLike maps LIKE onto MongoDB $regex with SQL wildcards
// (% → .*, _ → .) and anchors the pattern. The match is
// case-insensitive ($options "i") for parity with most SQL engines'
// default behavior on LIKE.
func (v *visitor) translateLike(expr *filter.BinaryExpr) (map[string]any, error) {
	field, err := v.fieldPath(expr)
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w (at %s)", err, expr.Start().String())
	}

	value, err := expr.Value()
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w (at %s)", err, expr.Start().String())
	}
	pattern, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("mongodb: LIKE requires a string pattern, got %T at %s",
			value, expr.Start().String())
	}

	var b strings.Builder
	b.WriteByte('^')
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteByte('.')
		// Escape regex metacharacters so the source pattern remains literal.
		case '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('$')

	// No "i" option: LIKE is case-sensitive, and $options "i" would answer a
	// wider question than the filter asked. The ^...$ anchors are what make
	// this a whole-value match rather than a substring one.
	return map[string]any{
		field: map[string]any{"$regex": b.String()},
	}, nil
}

// fieldPath assembles the dotted field path used by MongoDB.
func (v *visitor) fieldPath(expr *filter.BinaryExpr) (string, error) {
	keys, err := expr.Path()
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("empty key path on left operand")
	}
	if v.metadataPrefix == "" {
		return strings.Join(keys, "."), nil
	}
	return v.metadataPrefix + "." + strings.Join(keys, "."), nil
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
