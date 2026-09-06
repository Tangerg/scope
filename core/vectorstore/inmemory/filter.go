package inmemory

import (
	"fmt"
	"math"
	"reflect"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const firstInvalidSignedIndex = 1 << 63

type evaluator struct {
	metadata map[string]any
	match    bool
}

var _ filter.Visitor = (*evaluator)(nil)

func (e *evaluator) Visit(predicate filter.Predicate) error {
	value, err := e.eval(predicate)
	if err != nil {
		return err
	}
	match, ok := value.(bool)
	if !ok {
		return fmt.Errorf("inmemory: evaluate filter: predicate yielded %T, want bool", value)
	}
	e.match = match
	return nil
}

// matchesFilter returns whether metadata satisfies expr. Evaluation
// errors (type mismatch, unsupported node, etc.) are surfaced rather
// than swallowed — a malformed filter is a programmer bug.
func matchesFilter(expr filter.Predicate, metadata map[string]any) (bool, error) {
	visitor := evaluator{metadata: metadata}
	if err := expr.Accept(&visitor); err != nil {
		return false, err
	}
	return visitor.match, nil
}

func (e *evaluator) eval(expr filter.Expr) (any, error) {
	switch node := expr.(type) {
	case *filter.Ident:
		return e.lookupField(node.Name()), nil
	case *filter.Literal:
		return e.literalValue(node)
	case *filter.ListLiteral:
		return e.listValue(node)
	case *filter.IndexExpr:
		return e.evalIndex(node)
	case *filter.UnaryExpr:
		return e.evalUnary(node)
	case *filter.BinaryExpr:
		return e.evalBinary(node)
	}
	return nil, fmt.Errorf("inmemory: evaluate filter: unsupported node %T", expr)
}

func (e *evaluator) literalValue(lit *filter.Literal) (any, error) {
	value, err := lit.Value()
	if err != nil {
		return nil, fmt.Errorf("inmemory: evaluate filter: decode literal: %w", err)
	}
	return value, nil
}

func (e *evaluator) listValue(list *filter.ListLiteral) (any, error) {
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
func (e *evaluator) evalIndex(idx *filter.IndexExpr) (any, error) {
	keys, err := e.indexKeys(idx)
	if err != nil {
		return nil, err
	}
	var cur any = e.metadata
	for _, key := range keys {
		switch typed := cur.(type) {
		case map[string]any:
			s, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("inmemory: evaluate index: map key must be string, got %T", key)
			}
			cur = typed[s]
		case []any:
			index, ok := arrayIndex(key)
			if !ok {
				return nil, fmt.Errorf("inmemory: evaluate index: invalid array index %v (%T)", key, key)
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

func (e *evaluator) indexKeys(idx *filter.IndexExpr) ([]any, error) {
	var chain []any
	cur := filter.Expr(idx)
	for {
		switch typed := cur.(type) {
		case *filter.IndexExpr:
			key, err := e.literalValue(typed.Index())
			if err != nil {
				return nil, err
			}
			chain = append([]any{key}, chain...)
			cur = typed.Left()
		case *filter.Ident:
			chain = append([]any{typed.Name()}, chain...)
			return chain, nil
		default:
			return nil, fmt.Errorf("inmemory: evaluate filter: unexpected index base %T", cur)
		}
	}
}

func (e *evaluator) evalUnary(u *filter.UnaryExpr) (any, error) {
	if u.Operator() != filter.OpNot {
		return nil, fmt.Errorf("inmemory: evaluate unary expression: unsupported unary operator %s", u.Operator())
	}
	v, err := e.eval(u.Right())
	if err != nil {
		return nil, err
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("inmemory: evaluate unary expression: NOT operand must be bool, got %T", v)
	}
	return !b, nil
}

func (e *evaluator) evalBinary(b *filter.BinaryExpr) (any, error) {
	switch b.Operator() {
	case filter.OpAnd, filter.OpOr:
		return e.evalLogical(b)
	case filter.OpEqual, filter.OpNotEqual:
		return e.evalEquality(b)
	case filter.OpLess, filter.OpLessEqual, filter.OpGreater, filter.OpGreaterEqual:
		return e.evalOrdering(b)
	case filter.OpIn:
		return e.evalIn(b)
	case filter.OpHas:
		return e.evalHas(b)
	case filter.OpLike:
		return e.evalLike(b)
	case filter.OpIs:
		return e.evalNullTest(b)
	}
	return nil, fmt.Errorf("inmemory: evaluate binary expression: unsupported binary operator %s", b.Operator())
}

func (e *evaluator) evalHas(b *filter.BinaryExpr) (any, error) {
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
func (e *evaluator) evalNullTest(b *filter.BinaryExpr) (any, error) {
	left, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	return left == nil, nil
}

func (e *evaluator) evalLogical(b *filter.BinaryExpr) (any, error) {
	left, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	lb, ok := left.(bool)
	if !ok {
		return nil, fmt.Errorf("inmemory: evaluate logical expression: %s left operand must be bool, got %T", b.Operator(), left)
	}
	// Short-circuit.
	if b.Operator() == filter.OpAnd && !lb {
		return false, nil
	}
	if b.Operator() == filter.OpOr && lb {
		return true, nil
	}
	right, err := e.eval(b.Right())
	if err != nil {
		return nil, err
	}
	rb, ok := right.(bool)
	if !ok {
		return nil, fmt.Errorf("inmemory: evaluate logical expression: %s right operand must be bool, got %T", b.Operator(), right)
	}
	return rb, nil
}

func (e *evaluator) evalEquality(b *filter.BinaryExpr) (any, error) {
	left, err := e.eval(b.Left())
	if err != nil {
		return nil, err
	}
	right, err := e.eval(b.Right())
	if err != nil {
		return nil, err
	}
	eq := equalValues(left, right)
	if b.Operator() == filter.OpNotEqual {
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

func (e *evaluator) evalOrdering(b *filter.BinaryExpr) (any, error) {
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
		return nil, fmt.Errorf("inmemory: evaluate ordering expression: %s left operand must be numeric, got %T", b.Operator(), left)
	}
	if !ordered {
		return false, nil
	}
	switch b.Operator() {
	case filter.OpLess:
		return order < 0, nil
	case filter.OpLessEqual:
		return order <= 0, nil
	case filter.OpGreater:
		return order > 0, nil
	case filter.OpGreaterEqual:
		return order >= 0, nil
	}
	return nil, fmt.Errorf("inmemory: evaluate ordering expression: unreachable op %s", b.Operator())
}

type integerValue struct {
	signed bool
	i      int64
	u      uint64
}

func compareNumbers(left, right any) (order int, numeric, ordered bool) {
	leftInteger, leftIsInteger := asInteger(left)
	rightInteger, rightIsInteger := asInteger(right)
	if leftIsInteger && rightIsInteger {
		return compareIntegers(leftInteger, rightInteger), true, true
	}

	leftFloat, leftIsNumber := toFloat(left)
	rightFloat, rightIsNumber := toFloat(right)
	if !leftIsNumber || !rightIsNumber {
		return 0, false, false
	}
	if math.IsNaN(leftFloat) || math.IsNaN(rightFloat) {
		return 0, true, false
	}
	switch {
	case leftFloat < rightFloat:
		return -1, true, true
	case leftFloat > rightFloat:
		return 1, true, true
	default:
		return 0, true, true
	}
}

func asInteger(value any) (integerValue, bool) {
	switch number := value.(type) {
	case int:
		return integerValue{signed: true, i: int64(number)}, true
	case int8:
		return integerValue{signed: true, i: int64(number)}, true
	case int16:
		return integerValue{signed: true, i: int64(number)}, true
	case int32:
		return integerValue{signed: true, i: int64(number)}, true
	case int64:
		return integerValue{signed: true, i: number}, true
	case uint:
		return integerValue{u: uint64(number)}, true
	case uint8:
		return integerValue{u: uint64(number)}, true
	case uint16:
		return integerValue{u: uint64(number)}, true
	case uint32:
		return integerValue{u: uint64(number)}, true
	case uint64:
		return integerValue{u: number}, true
	default:
		return integerValue{}, false
	}
}

func compareIntegers(left, right integerValue) int {
	if left.signed && right.signed {
		return compareInt64(left.i, right.i)
	}
	if !left.signed && !right.signed {
		return compareUint64(left.u, right.u)
	}
	if left.signed {
		if left.i < 0 {
			return -1
		}
		return compareUint64(uint64(left.i), right.u)
	}
	if right.i < 0 {
		return 1
	}
	return compareUint64(left.u, uint64(right.i))
}

func compareInt64(left, right int64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareUint64(left, right uint64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func (e *evaluator) evalIn(b *filter.BinaryExpr) (any, error) {
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
		return nil, fmt.Errorf("inmemory: evaluate membership: right operand must be list, got %T", right)
	}
	for _, item := range list {
		if equalValues(left, item) {
			return true, nil
		}
	}
	return false, nil
}

func (e *evaluator) evalLike(b *filter.BinaryExpr) (any, error) {
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
		return nil, fmt.Errorf("inmemory: evaluate pattern: LIKE left operand must be string, got %T", left)
	}
	pattern, ok := right.(string)
	if !ok {
		return nil, fmt.Errorf("inmemory: evaluate pattern: LIKE right operand must be string, got %T", right)
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
	if e.metadata == nil {
		return nil
	}
	return e.metadata[name]
}

// toFloat is the mixed integer/decimal fallback. Integer-only comparisons use
// compareIntegers so values above float64's exact range do not collapse.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	// Numeric strings stay strings — `"12" < "9"` should fail loudly,
	// not silently coerce.
	return 0, false
}
