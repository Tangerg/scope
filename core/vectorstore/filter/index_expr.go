package filter

import (
	"errors"
	"fmt"
)

type IndexExpr struct {
	left  Selector
	index *Literal
	start Position
	end   Position
}

func (*IndexExpr) expr()     {}
func (*IndexExpr) selector() {}

func (i *IndexExpr) Left() Selector {
	if i == nil {
		return nil
	}
	return i.left
}

func (i *IndexExpr) Index() *Literal {
	if i == nil {
		return nil
	}
	return i.index
}

func (i *IndexExpr) Path() ([]PathSegment, error) {
	if err := i.validate(); err != nil {
		return nil, err
	}
	path, err := i.left.Path()
	if err != nil {
		return nil, err
	}
	segment, err := i.index.pathSegment()
	if err != nil {
		return nil, err
	}
	return append(path, segment), nil
}

func (i *IndexExpr) Start() Position {
	if i == nil {
		return Position{}
	}
	return i.start
}

func (i *IndexExpr) End() Position {
	if i == nil {
		return Position{}
	}
	return i.end
}

func (i *IndexExpr) Equal(other Expr) bool {
	o, ok := other.(*IndexExpr)
	return ok && i != nil && o != nil && equalExpr(i.left, o.left) && equalExpr(i.index, o.index)
}

func (i *IndexExpr) validate() error {
	if i == nil {
		return errors.New("filter: index expression is nil")
	}
	if err := validateSelector(i.left); err != nil {
		return fmt.Errorf("filter: index base: %w", err)
	}
	if i.index == nil {
		return fmt.Errorf("filter: index is nil at %s", i.Start())
	}
	if err := i.index.validate(); err != nil {
		return err
	}
	_, err := i.index.pathSegment()
	return err
}
