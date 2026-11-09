package filter

import (
	"errors"
	"fmt"

	"github.com/samber/lo"
)

type Expr interface {
	// Start returns the inclusive source position of the first token. Trees built
	// programmatically use a zero position.
	Start() Position
	// End returns the exclusive source position after the last token. Trees built
	// programmatically use a zero position.
	End() Position
	// Equal compares semantic tree shape and values, ignoring pointer identity.
	// It must be total for every valid Expr implementation.
	Equal(Expr) bool
	snapshot(*treeSnapshot) Expr
}

type Selector interface {
	Expr
	// Path returns an independently owned metadata path whose first segment is
	// the identifier key. It fails when the selector contains an invalid
	// identifier or index expression.
	Path() ([]PathSegment, error)
	selector()
}

// Predicate is a complete boolean expression. Clone it before giving its nodes
// to a collaborator that must not change the original query.
type Predicate interface {
	Expr
	fmt.Stringer
	// Validate proves the complete subtree is structurally legal before a
	// visitor, formatter, or backend compiler observes it.
	Validate() error
	// Accept dispatches the complete validated predicate to visitor. Traversal
	// order belongs to the visitor; nil visitors and invalid trees are rejected.
	Accept(Visitor) error
	predicate()
}

func equalExpr(left, right Expr) bool {
	leftNil := lo.IsNil(left)
	rightNil := lo.IsNil(right)
	if leftNil || rightNil {
		return leftNil && rightNil
	}
	return left.Equal(right)
}

// maxPredicateDepth bounds how deeply predicates nest, counting both
// parentheses and the left-deep chains that AND and OR build. Formatting,
// comparison, optimization, matching and backend compilers recurse over the
// tree, and a stack overflow is a fatal error no caller can recover from, so
// a tree from untrusted input must be rejected before any of them walks it.
const maxPredicateDepth = 1024

func validatePredicate(root Predicate) error {
	type frame struct {
		node   Predicate
		depth  int
		finish bool
	}
	// Reusing a predicate in two branches is valid. Only an edge back into
	// the current path is a cycle.
	finished := make(map[Predicate]bool)
	stack := []frame{{node: root, depth: 1}}
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		switch current.node.(type) {
		case *BinaryExpr, *UnaryExpr:
		default:
			return fmt.Errorf("filter: unsupported predicate %T", current.node)
		}
		if current.depth > maxPredicateDepth {
			return fmt.Errorf("filter: predicate nests deeper than %d levels", maxPredicateDepth)
		}
		if current.finish {
			finished[current.node] = true
			continue
		}
		if done, seen := finished[current.node]; seen {
			if !done {
				return errors.New("filter: cyclic predicate")
			}
			continue
		}
		finished[current.node] = false
		stack = append(stack, frame{node: current.node, finish: true})
		switch node := current.node.(type) {
		case *BinaryExpr:
			if err := node.validate(); err != nil {
				return err
			}
			if node.operator.IsLogicalOperator() {
				child := current.depth + 1
				stack = append(stack, frame{node: node.right.(Predicate), depth: child}, frame{node: node.left.(Predicate), depth: child})
			}
		case *UnaryExpr:
			if err := node.validate(); err != nil {
				return err
			}
			stack = append(stack, frame{node: node.right, depth: current.depth + 1})
		}
	}
	return nil
}

func validateSelector(expr Expr) error {
	switch node := expr.(type) {
	case *Ident:
		return node.validate()
	case *IndexExpr:
		return node.validate()
	default:
		if lo.IsNil(expr) {
			return errors.New("selector is nil")
		}
		return fmt.Errorf("expected identifier or index, got %T at %s", expr, expr.Start())
	}
}
