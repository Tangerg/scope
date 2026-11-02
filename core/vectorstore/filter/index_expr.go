package filter

import (
	"errors"
	"fmt"
	"slices"
)

type IndexExpr struct {
	left  Selector
	index *Literal
	start Position
	end   Position
}

func (i *IndexExpr) snapshot(tree *treeSnapshot) Expr { return tree.retain(i) }

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
	var segments []PathSegment
	seen := make(map[*IndexExpr]bool)
	for node := i; node != nil; {
		if seen[node] {
			return nil, errors.New("filter: cyclic selector")
		}
		seen[node] = true
		if node.index == nil {
			return nil, fmt.Errorf("filter: index is nil at %s", node.Start())
		}
		if err := node.index.validate(); err != nil {
			return nil, err
		}
		segment, err := node.index.pathSegment()
		if err != nil {
			return nil, err
		}
		segments = append(segments, segment)
		switch base := node.left.(type) {
		case *IndexExpr:
			node = base
		case *Ident:
			if err := base.validate(); err != nil {
				return nil, err
			}
			segments = append(segments, PathSegment{key: base.name})
			slices.Reverse(segments)
			return segments, nil
		default:
			return nil, fmt.Errorf("filter: invalid index base %T", base)
		}
	}
	return nil, errors.New("filter: index expression is nil")
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
	_, err := i.Path()
	return err
}
