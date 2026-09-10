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

// BinaryExpr combines two expressions with a comparison, logical, matching,
// or null-test operator.
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

// Path returns the complete key path selected by the left operand.
//
// The segments are whatever the filter selected: a bare identifier is
// constrained by the grammar, but an indexed key is a string literal, so a
// segment can hold anything a string can. A compiler that binds the segment as
// a value — a SQL map subscript, a BSON field name, a JSON path argument — can
// use it as it is, because a bound value cannot leave the position it was bound
// to. A compiler that pastes it into query text must use [BinaryExpr.IdentifierPath]
// instead.
func (b *BinaryExpr) Path() ([]string, error) {
	selector, err := b.Selector()
	if err != nil {
		return nil, err
	}
	return selector.Path()
}

// IdentifierPath returns the key path when every segment is a plain
// identifier, and reports the first segment that is not.
//
// It exists because a compiler that pastes a path into query text cannot use
// [BinaryExpr.Path]: an indexed key is a string literal, so the caller chooses
// the bytes, and the target language reads them as syntax.
// metadata['a:1 OR b'] == 'x' compiled to Lucene as metadata.a:1 OR b:"x",
// where a key turned into a term boundary and a boolean operator; the same
// shape reaches Typesense's filter_by, Vespa's YQL, an OData filter and a
// RediSearch tag clause. None of those languages can quote or escape a field
// name, so a segment they cannot express has to be refused rather than
// approximated.
//
// A plain identifier is a letter or underscore followed by letters, digits or
// underscores — the same shape the stores that interpolate configured column
// names already require of them. A store can still hold a document whose
// metadata key is anything at all; this is only about which keys that store
// can name in a filter.
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

// Value decodes the scalar right operand using its exact semantic type.
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

func (b *BinaryExpr) Validate() error              { return validatePredicate(b) }
func (b *BinaryExpr) Accept(visitor Visitor) error { return accept(b, visitor) }
func (b *BinaryExpr) String() string               { return formatPredicate(b) }

// Inverse returns an equivalent binary expression using the exact inverse
// comparison operator. The receiver is not mutated.
func (b *BinaryExpr) Inverse() (*BinaryExpr, error) {
	if b == nil {
		return nil, errors.New("filter: invert expression: expression is nil")
	}
	operator, err := b.operator.Inverse()
	if err != nil {
		return nil, err
	}
	return &BinaryExpr{left: b.left, operator: operator, right: b.right, start: b.start, end: b.end}, nil
}

// Dispatch routes the expression to the handler for its operator family.
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
	case b.operator.Is(OpIn):
		handler = handlers.In
	case b.operator.Is(OpHas):
		handler = handlers.Has
	case b.operator.Is(OpLike):
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
