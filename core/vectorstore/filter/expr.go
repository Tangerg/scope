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
	expr()
}

type Selector interface {
	Expr
	// Path returns an independently owned metadata path. It fails when the
	// selector contains an invalid identifier or index expression.
	Path() ([]string, error)
	selector()
}

// Predicate is a complete, immutable boolean expression.
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

func validateSelector(expr Expr) error {
	if lo.IsNil(expr) {
		return errors.New("selector is nil")
	}
	switch node := expr.(type) {
	case *Ident:
		return node.validate()
	case *IndexExpr:
		return node.validate()
	default:
		return fmt.Errorf("expected identifier or index, got %T at %s", expr, expr.Start())
	}
}
