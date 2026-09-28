package filter

import (
	"fmt"
)

type IdentifierValue interface {
	string | *Ident
}

func newIdent(value any) (*Ident, error) {
	switch typed := value.(type) {
	case string:
		return &Ident{name: typed}, nil
	case *Ident:
		return typed, nil
	default:
		return nil, fmt.Errorf("filter: create identifier: expected string or *filter.Ident, got %T (%v)",
			value, value)
	}
}

func NewIdent[T IdentifierValue](value T) *Ident {
	ident, err := newIdent(value)
	if err != nil {
		panic(fmt.Errorf("filter: create identifier: %w", err))
	}
	return ident
}

func identOrIndex(l any) (Selector, error) {
	if ix, ok := l.(*IndexExpr); ok {
		return ix, nil
	}
	return newIdent(l)
}

func leftOperand[L IdentifierValue | *IndexExpr](l L) Selector {
	expr, _ := identOrIndex(l)
	return expr
}
