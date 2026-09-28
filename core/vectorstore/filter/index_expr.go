package filter

import (
	"errors"
	"fmt"

	"github.com/samber/lo"
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

func (i *IndexExpr) Path() ([]string, error) {
	if i == nil {
		return nil, errors.New("filter: read index path: index expression is nil")
	}
	if lo.IsNil(i.left) {
		return nil, errors.New("filter: read index path: base is nil")
	}
	path, err := i.left.Path()
	if err != nil {
		return nil, err
	}
	key, err := i.index.Key()
	if err != nil {
		return nil, err
	}
	return append(path, key), nil
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
	if !i.index.IsString() && !i.index.IsNumber() {
		return fmt.Errorf("filter: index must be a string or number, got %s at %s", i.index.Kind(), i.index.Start())
	}
	if err := i.index.validate(); err != nil {
		return err
	}
	if i.index.IsNumber() && !i.index.isIntegerIndex() {
		return fmt.Errorf("filter: numeric index must be a non-negative integer, got %q at %s", i.index.Text(), i.index.Start())
	}
	return nil
}
