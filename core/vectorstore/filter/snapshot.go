package filter

import (
	"slices"

	"github.com/samber/lo"
)

// Clone isolates the tree before an external compiler receives its concrete
// nodes. Even a node with private fields can be overwritten through a pointer.
func Clone(predicate Predicate) Predicate {
	// Nodes may be shared or cyclic before validation. Allocate each once before
	// reconnecting its outgoing edges, preserving sharing without recursive calls.
	snapshot := treeSnapshot{copies: make(map[Expr]Expr)}
	root := snapshot.clone(predicate)
	for len(snapshot.pending) > 0 {
		last := len(snapshot.pending) - 1
		node := snapshot.pending[last]
		snapshot.pending = snapshot.pending[:last]
		switch n := node.(type) {
		case *BinaryExpr:
			n.left = snapshot.clone(n.left)
			n.right = snapshot.clone(n.right)
		case *UnaryExpr:
			n.right = snapshot.clone(n.right)
		case *IndexExpr:
			n.left = snapshot.clone(n.left)
			n.index = snapshot.clone(n.index)
		case *ListLiteral:
			n.values = slices.Clone(n.values)
			for i, value := range n.values {
				n.values[i] = snapshot.clone(value)
			}
		}
	}
	return root
}

type treeSnapshot struct {
	copies  map[Expr]Expr
	pending []Expr
}

func (t *treeSnapshot) clone[T Expr](node T) T {
	if lo.IsNil(node) {
		return node
	}
	return node.snapshot(t).(T)
}

func (t *treeSnapshot) retain[V any, N interface {
	*V
	Expr
}](node N) Expr {
	if node == nil {
		return node
	}
	if existing, ok := t.copies[node]; ok {
		return existing
	}
	cloned := N(new(*node))
	t.copies[node] = cloned
	t.pending = append(t.pending, cloned)
	return cloned
}
