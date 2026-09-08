package filter

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
)

type evaluator struct {
	values map[string]any
	match  bool
}

var _ Visitor = (*evaluator)(nil)

func (e *evaluator) Visit(predicate Predicate) error {
	value, err := e.eval(predicate)
	if err != nil {
		return err
	}
	match, ok := value.(bool)
	if !ok {
		return fmt.Errorf("filter: evaluate filter: predicate yielded %T, want bool", value)
	}
	e.match = match
	return nil
}

// Match reports whether values satisfies expr. It is the canonical
// client-side evaluation of a Predicate, used by stores whose provider cannot
// express the filter and by any caller that must decide membership locally.
//
// values holds decoded metadata; [github.com/Tangerg/scope/core/metadata.Map]
// Values decodes numbers as json.Number, which this evaluator compares exactly
// rather than through float64. Evaluation errors (type mismatch, unsupported
// node) are surfaced rather than swallowed, because a malformed filter is a
// programmer bug and silently reporting "no match" would delete or omit the
// wrong documents.
func Match(expr Predicate, values map[string]any) (bool, error) {
	visitor := evaluator{values: values}
	if err := expr.Accept(&visitor); err != nil {
		return false, err
	}
	return visitor.match, nil
}

func (e *evaluator) eval(expr Expr) (any, error) {
	switch node := expr.(type) {
	case *Ident:
		return e.lookupField(node.Name()), nil
	case *Literal:
		return e.literalValue(node)
	case *ListLiteral:
		return e.listValue(node)
	case *IndexExpr:
		return e.evalIndex(node)
	case *UnaryExpr:
		return e.evalUnary(node)
	case *BinaryExpr:
		return e.evalBinary(node)
	}
	return nil, fmt.Errorf("filter: evaluate filter: unsupported node %T", expr)
}

func (e *evaluator) literalValue(lit *Literal) (any, error) {
	value, err := lit.Value()
	if err != nil {
		return nil, fmt.Errorf("filter: evaluate filter: decode literal: %w", err)
	}
	return value, nil
}

func (e *evaluator) listValue(list *ListLiteral) (any, error) {
	out := make([]any, 0, list.Len())
	for _, item := range list.Literals() {
		v, err := e.literalValue(item)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// evalIndex resolves an `a["b"][0]`-style chain. Missing keys / OOB
// indices return nil (matching SQL NULL semantics); only structural
// type errors are reported.
func (e *evaluator) evalIndex(idx *IndexExpr) (any, error) {
	keys, err := e.indexKeys(idx)
	if err != nil {
		return nil, err
	}
	var cur any = e.values
	for _, key := range keys {
		switch typed := cur.(type) {
		case map[string]any:
			s, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("filter: evaluate index: map key must be string, got %T", key)
			}
			cur = typed[s]
		case []any:
			index, ok := arrayIndex(key)
			if !ok {
				return nil, fmt.Errorf("filter: evaluate index: invalid array index %v (%T)", key, key)
			}
			if index >= uint64(len(typed)) {
				return nil, nil
			}
			cur = typed[int(index)]
		default:
			return nil, nil
		}
	}
	return cur, nil
}

func arrayIndex(value any) (uint64, bool) {
	switch number := value.(type) {
	case int64:
		if number < 0 {
			return 0, false
		}
		return uint64(number), true
	case uint64:
		return number, true
	case float64:
		if number < 0 || number >= firstInvalidSignedIndex || math.Trunc(number) != number {
			return 0, false
		}
		return uint64(number), true
	default:
		return 0, false
	}
}

func (e *evaluator) indexKeys(idx *IndexExpr) ([]any, error) {
	var chain []any
	cur := Expr(idx)
	for {
		switch typed := cur.(type) {
		case *IndexExpr:
			key, err := e.literalValue(typed.Index())
			if err != nil {
				return nil, err
			}
			chain = append([]any{key}, chain...)
			cur = typed.Left()
		case *Ident:
			chain = append([]any{typed.Name()}, chain...)
			return chain, nil
		default:
			return nil, fmt.Errorf("filter: evaluate filter: unexpected index base %T", cur)
		}
	}
}

func (e *evaluator) evalUnary(u *UnaryExpr) (any, error) {
	if u.Operator() != OpNot {
		return nil, fmt.Errorf("filter: evaluate unary expression: unsupported unary operator %s", u.Operator())
	}
	v, err := e.eval(u.Right())
	if err != nil {
		return nil, err
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("filter: evaluate unary expression: NOT operand must be bool, got %T", v)
	}
	return !b, nil
}

func (e *evaluator) evalBinary(b *BinaryExpr) (any, error) {
	switch b.Operator() {
	case OpAnd, OpOr:
		return e.evalLogical(b)
	case OpEqual, OpNotEqual:
		return e.evalEquality(b)
	case OpLess, OpLessEqual, OpGreater, OpGreaterEqual:
		return e.evalOrdering(b)
	case OpIn:
		return e.evalIn(b)
	case OpHas:
		return e.evalHas(b)
	case OpLike:
		return e.evalLike(b)
	case OpIs:
		return e.evalNullTest(b)
	}
	return nil, fmt.Errorf("filter: evaluate binary expression: unsupported binary operator %s", b.Operator())
}

func (e *evaluator) evalHas(b *BinaryExpr) (any, error) {
	collection, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	wanted, err := e.eval(b.Right())
	if err != nil {
		return nil, err
	}
	if collection == nil {
		return false, nil
	}

	value := reflect.ValueOf(collection)
	if value.Kind() != reflect.Array && value.Kind() != reflect.Slice {
		return false, nil
	}
	for index := range value.Len() {
		if equalValues(value.Index(index).Interface(), wanted) {
			return true, nil
		}
	}
	return false, nil
}

// Missing metadata and explicit nil intentionally share filter semantics so
// in-memory evaluation agrees with provider-side IS NULL translations.
func (e *evaluator) evalNullTest(b *BinaryExpr) (any, error) {
	left, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	return left == nil, nil
}

func (e *evaluator) evalLogical(b *BinaryExpr) (any, error) {
	left, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	lb, ok := left.(bool)
	if !ok {
		return nil, fmt.Errorf("filter: evaluate logical expression: %s left operand must be bool, got %T", b.Operator(), left)
	}
	// Short-circuit.
	if b.Operator() == OpAnd && !lb {
		return false, nil
	}
	if b.Operator() == OpOr && lb {
		return true, nil
	}
	right, err := e.eval(b.Right())
	if err != nil {
		return nil, err
	}
	rb, ok := right.(bool)
	if !ok {
		return nil, fmt.Errorf("filter: evaluate logical expression: %s right operand must be bool, got %T", b.Operator(), right)
	}
	return rb, nil
}

func (e *evaluator) evalEquality(b *BinaryExpr) (any, error) {
	left, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	right, err := e.eval(b.Right())
	if err != nil {
		return nil, err
	}
	eq := equalValues(left, right)
	if b.Operator() == OpNotEqual {
		return !eq, nil
	}
	return eq, nil
}

func equalValues(a, b any) bool {
	if a == nil || b == nil {
		return a == b
	}
	if order, numeric, ordered := compareNumbers(a, b); numeric {
		return ordered && order == 0
	}
	return a == b
}

func (e *evaluator) evalOrdering(b *BinaryExpr) (any, error) {
	left, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	if left == nil {
		return false, nil
	}
	right, err := e.eval(b.Right())
	if err != nil {
		return nil, err
	}
	order, numeric, ordered := compareNumbers(left, right)
	if !numeric {
		return nil, fmt.Errorf("filter: evaluate ordering expression: %s left operand must be numeric, got %T", b.Operator(), left)
	}
	if !ordered {
		return false, nil
	}
	switch b.Operator() {
	case OpLess:
		return order < 0, nil
	case OpLessEqual:
		return order <= 0, nil
	case OpGreater:
		return order > 0, nil
	case OpGreaterEqual:
		return order >= 0, nil
	}
	return nil, fmt.Errorf("filter: evaluate ordering expression: unreachable op %s", b.Operator())
}

func compareNumbers(left, right any) (order int, numeric, ordered bool) {
	leftNumber, leftIsNumber := numberValue(left)
	rightNumber, rightIsNumber := numberValue(right)
	if !leftIsNumber || !rightIsNumber {
		return 0, false, false
	}
	if leftNumber == nil || rightNumber == nil {
		return 0, true, false
	}
	return leftNumber.Cmp(rightNumber), true, true
}

// Rational conversion preserves each number's value, including a float's
// binary fraction, without rounding an integer to the float's precision.
func numberValue(value any) (*big.Rat, bool) {
	switch number := value.(type) {
	case int:
		return new(big.Rat).SetInt64(int64(number)), true
	case int8:
		return new(big.Rat).SetInt64(int64(number)), true
	case int16:
		return new(big.Rat).SetInt64(int64(number)), true
	case int32:
		return new(big.Rat).SetInt64(int64(number)), true
	case int64:
		return new(big.Rat).SetInt64(number), true
	case uint:
		return new(big.Rat).SetUint64(uint64(number)), true
	case uint8:
		return new(big.Rat).SetUint64(uint64(number)), true
	case uint16:
		return new(big.Rat).SetUint64(uint64(number)), true
	case uint32:
		return new(big.Rat).SetUint64(uint64(number)), true
	case uint64:
		return new(big.Rat).SetUint64(number), true
	case float32:
		return new(big.Rat).SetFloat64(float64(number)), true
	case float64:
		return new(big.Rat).SetFloat64(number), true
	case json.Number:
		return new(big.Rat).SetString(number.String())
	default:
		return nil, false
	}
}

func (e *evaluator) evalIn(b *BinaryExpr) (any, error) {
	left, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	right, err := e.eval(b.Right())
	if err != nil {
		return nil, err
	}
	list, ok := right.([]any)
	if !ok {
		return nil, fmt.Errorf("filter: evaluate membership: right operand must be list, got %T", right)
	}
	for _, item := range list {
		if equalValues(left, item) {
			return true, nil
		}
	}
	return false, nil
}

func (e *evaluator) evalLike(b *BinaryExpr) (any, error) {
	left, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	if left == nil {
		return false, nil
	}
	right, err := e.eval(b.Right())
	if err != nil {
		return nil, err
	}
	s, ok := left.(string)
	if !ok {
		return nil, fmt.Errorf("filter: evaluate pattern: LIKE left operand must be string, got %T", left)
	}
	pattern, ok := right.(string)
	if !ok {
		return nil, fmt.Errorf("filter: evaluate pattern: LIKE right operand must be string, got %T", right)
	}
	return likeMatch(s, pattern), nil
}

// likeMatch is SQL LIKE: % matches any run of characters, _ matches
// one. The pattern must match the whole input. Greedy backtracking is
// acceptable here because metadata strings are short.
func likeMatch(s, pattern string) bool {
	return likeMatchRunes([]rune(s), []rune(pattern))
}

func likeMatchRunes(s, p []rune) bool {
	si, pi := 0, 0
	starS, starP := -1, -1
	for si < len(s) {
		switch {
		case pi < len(p) && p[pi] == '%':
			starP = pi
			starS = si
			pi++
		case pi < len(p) && (p[pi] == '_' || p[pi] == s[si]):
			si++
			pi++
		case starP != -1:
			pi = starP + 1
			starS++
			si = starS
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '%' {
		pi++
	}
	return pi == len(p)
}

// lookupField returns nil for absent fields. IS NULL treats that as null;
// ordering and pattern predicates treat it as a non-match.
func (e *evaluator) lookupField(name string) any {
	if e.values == nil {
		return nil
	}
	return e.values[name]
}
