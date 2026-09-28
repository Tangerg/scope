package filter

import (
	"errors"
	"fmt"

	"github.com/samber/lo"
)

// BinaryHandlers names the operator-specific branches of a filter compiler.
// A nil handler means that the backend does not support that operator.
type BinaryHandlers struct {
	Logical    func(*BinaryExpr) error
	Comparison func(*BinaryExpr) error
	In         func(*BinaryExpr) error
	Has        func(*BinaryExpr) error
	Like       func(*BinaryExpr) error
	NullTest   func(*BinaryExpr) error
}

type BinaryExpr struct {
	left     Expr
	operator Operator
	right    Expr
	start    Position
	end      Position
}

func (*BinaryExpr) expr()      {}
func (*BinaryExpr) predicate() {}

func (b *BinaryExpr) Left() Expr {
	if b == nil {
		return nil
	}
	return b.left
}

func (b *BinaryExpr) Operator() Operator {
	if b == nil {
		return ""
	}
	return b.operator
}

func (b *BinaryExpr) Right() Expr {
	if b == nil {
		return nil
	}
	return b.right
}

func (b *BinaryExpr) Selector() (Selector, error) {
	if b == nil {
		return nil, errors.New("filter: read comparison selector: expression is nil")
	}
	selector, ok := b.left.(Selector)
	if !ok || lo.IsNil(selector) {
		return nil, fmt.Errorf("filter: %s requires a selector on the left at %s, got %T",
			b.operator.Name(), b.Start(), b.left)
	}
	return selector, nil
}

// Path returns the selected metadata keys. Indexed keys may contain arbitrary
// valid UTF-8, so compilers may bind them as values but must use IdentifierPath
// before interpolating them as query syntax.
func (b *BinaryExpr) Path() ([]string, error) {
	selector, err := b.Selector()
	if err != nil {
		return nil, err
	}
	return selector.Path()
}

// IdentifierPath rejects segments outside [A-Za-z_][A-Za-z0-9_]*. Use it when
// a filter language cannot quote field names: interpolating an arbitrary indexed
// key would let metadata bytes become query syntax. Path remains available for
// compilers that bind keys as values.
func (b *BinaryExpr) IdentifierPath() ([]string, error) {
	keys, err := b.Path()
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("filter: read identifier path: empty key path at %s", b.Start())
	}
	for _, key := range keys {
		if !isPlainIdentifier(key) {
			return nil, fmt.Errorf(
				"filter: read identifier path: key %q at %s is not a plain identifier, so it cannot be named in this store's filter language",
				key, b.Start())
		}
	}
	return keys, nil
}

func isPlainIdentifier(key string) bool {
	if key == "" {
		return false
	}
	for index, character := range key {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character == '_':
		case character >= '0' && character <= '9':
			if index == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (b *BinaryExpr) Literal() (*Literal, error) {
	if b == nil {
		return nil, errors.New("filter: read comparison literal: expression is nil")
	}
	literal, ok := b.right.(*Literal)
	if !ok || literal == nil {
		return nil, fmt.Errorf("filter: %s requires a literal on the right at %s, got %T",
			b.operator.Name(), b.Start(), b.right)
	}
	return literal, nil
}

func (b *BinaryExpr) Value() (any, error) {
	literal, err := b.Literal()
	if err != nil {
		return nil, err
	}
	return literal.Value()
}

func (b *BinaryExpr) List() (*ListLiteral, error) {
	if b == nil {
		return nil, errors.New("filter: read membership list: expression is nil")
	}
	list, ok := b.right.(*ListLiteral)
	if !ok || list == nil {
		return nil, fmt.Errorf("filter: IN requires a list on the right at %s, got %T", b.Start(), b.right)
	}
	if list.Len() == 0 {
		return nil, fmt.Errorf("filter: IN requires a non-empty list at %s", b.Start())
	}
	return list, nil
}

func (b *BinaryExpr) Pattern() (string, error) {
	literal, err := b.Literal()
	if err != nil {
		return "", err
	}
	pattern, err := literal.AsString()
	if err != nil {
		return "", fmt.Errorf("filter: LIKE requires a string pattern at %s: %w", b.Start(), err)
	}
	return pattern, nil
}

func (b *BinaryExpr) Start() Position {
	if b == nil {
		return Position{}
	}
	return b.start
}

func (b *BinaryExpr) End() Position {
	if b == nil {
		return Position{}
	}
	return b.end
}

func (b *BinaryExpr) Equal(other Expr) bool {
	o, ok := other.(*BinaryExpr)
	return ok && b != nil && o != nil && b.operator == o.operator && equalExpr(b.left, o.left) && equalExpr(b.right, o.right)
}

func (b *BinaryExpr) Validate() error {
	if b == nil {
		return errors.New("filter: binary expression is nil")
	}
	if !b.operator.IsBinaryOperator() {
		return fmt.Errorf("filter: invalid binary operator %q at %s", b.operator, b.Start())
	}
	if lo.IsNil(b.left) {
		return fmt.Errorf("filter: %s left operand is nil at %s", b.operator.Name(), b.Start())
	}
	if lo.IsNil(b.right) {
		return fmt.Errorf("filter: %s right operand is nil at %s", b.operator.Name(), b.Start())
	}
	if b.operator.IsLogicalOperator() {
		return b.validateLogical()
	}
	if err := validateSelector(b.left); err != nil {
		return fmt.Errorf("filter: %s left operand: %w", b.operator.Name(), err)
	}

	switch {
	case b.operator.IsEqualityOperator():
		return b.validateComparison(false)
	case b.operator.IsOrderingOperator():
		return b.validateComparison(true)
	case b.operator == OpIn:
		return b.validateMembership()
	case b.operator == OpHas:
		return b.validateCollectionMembership()
	case b.operator == OpLike:
		return b.validateLike()
	case b.operator == OpIs:
		return b.validateNullTest()
	default:
		return fmt.Errorf("filter: unsupported binary operator %q at %s", b.operator, b.Start())
	}
}

func (b *BinaryExpr) Accept(visitor Visitor) error { return accept(b, visitor) }
func (b *BinaryExpr) String() string               { return formatPredicate(b) }

// Inverse negates a comparison, including missing and null metadata values.
// The receiver is not mutated.
func (b *BinaryExpr) Inverse() (*BinaryExpr, error) {
	if b == nil {
		return nil, errors.New("filter: invert expression: expression is nil")
	}
	operator, err := b.operator.inverseComparison()
	if err != nil {
		return nil, err
	}
	inverse := &BinaryExpr{left: b.left, operator: operator, right: b.right, start: b.start, end: b.end}
	if b.operator.IsOrderingOperator() {
		nullTest := &BinaryExpr{left: b.left, operator: OpIs, right: &Literal{kind: LiteralNull, text: "null"}, start: b.start, end: b.end}
		return Or(nullTest, inverse), nil
	}
	return inverse, nil
}

func (b *BinaryExpr) Dispatch(handlers BinaryHandlers) error {
	if b == nil {
		return errors.New("filter: dispatch binary expression: expression is nil")
	}
	var handler func(*BinaryExpr) error
	switch {
	case b.operator.IsLogicalOperator():
		handler = handlers.Logical
	case b.operator.IsComparisonOperator():
		handler = handlers.Comparison
	case b.operator == OpIn:
		handler = handlers.In
	case b.operator == OpHas:
		handler = handlers.Has
	case b.operator == OpLike:
		handler = handlers.Like
	case b.operator.IsNullOperator():
		handler = handlers.NullTest
	default:
		return fmt.Errorf("filter: unsupported binary operator %q at %s", b.operator, b.Start())
	}
	if handler == nil {
		return fmt.Errorf("filter: binary operator %s is not supported at %s", b.operator.Name(), b.Start())
	}
	return handler(b)
}

func (b *BinaryExpr) validateLogical() error {
	left, ok := b.left.(Predicate)
	if !ok {
		return fmt.Errorf("filter: %s left operand must be a predicate, got %T at %s", b.operator.Name(), b.left, b.Start())
	}
	right, ok := b.right.(Predicate)
	if !ok {
		return fmt.Errorf("filter: %s right operand must be a predicate, got %T at %s", b.operator.Name(), b.right, b.Start())
	}
	if err := left.Validate(); err != nil {
		return err
	}
	return right.Validate()
}

func (b *BinaryExpr) validateComparison(numeric bool) error {
	literal, ok := b.right.(*Literal)
	if !ok || literal == nil {
		return fmt.Errorf("filter: %s right operand must be a literal, got %T at %s", b.operator.Name(), b.right, b.Start())
	}
	if literal.IsNull() {
		return fmt.Errorf("filter: %s cannot compare NULL; use IS NULL at %s", b.operator.Name(), b.Start())
	}
	if numeric && !literal.IsNumber() {
		return fmt.Errorf("filter: %s right operand must be numeric, got %s at %s", b.operator.Name(), literal.kind, literal.Start())
	}
	return literal.validate()
}

func (b *BinaryExpr) validateMembership() error {
	list, ok := b.right.(*ListLiteral)
	if !ok || list == nil {
		return fmt.Errorf("filter: IN right operand must be a list, got %T at %s", b.right, b.Start())
	}
	return list.validate()
}

func (b *BinaryExpr) validateCollectionMembership() error {
	literal, ok := b.right.(*Literal)
	if !ok || literal == nil {
		return fmt.Errorf("filter: HAS right operand must be a literal, got %T at %s", b.right, b.Start())
	}
	if literal.IsNull() {
		return fmt.Errorf("filter: HAS cannot test NULL at %s", b.Start())
	}
	return literal.validate()
}

func (b *BinaryExpr) validateLike() error {
	literal, ok := b.right.(*Literal)
	if !ok || literal == nil || !literal.IsString() {
		return fmt.Errorf("filter: LIKE right operand must be a string literal, got %T at %s", b.right, b.Start())
	}
	return literal.validate()
}

func (b *BinaryExpr) validateNullTest() error {
	literal, ok := b.right.(*Literal)
	if !ok || literal == nil || !literal.IsNull() {
		return fmt.Errorf("filter: IS right operand must be NULL, got %T at %s", b.right, b.Start())
	}
	return literal.validate()
}
