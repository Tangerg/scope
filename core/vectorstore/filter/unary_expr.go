package filter

import (
	"errors"
	"fmt"
)

// UnaryExpr negates one predicate.
type UnaryExpr struct {
	operator Operator
	right    Predicate
	start    Position
	end      Position
}

func (*UnaryExpr) expr()      {}
func (*UnaryExpr) predicate() {}

func (u *UnaryExpr) Operator() Operator {
	if u == nil {
		return ""
	}
	return u.operator
}

func (u *UnaryExpr) Right() Predicate {
	if u == nil {
		return nil
	}
	return u.right
}

func (u *UnaryExpr) Start() Position {
	if u == nil {
		return Position{}
	}
	return u.start
}

func (u *UnaryExpr) End() Position {
	if u == nil {
		return Position{}
	}
	return u.end
}

func (u *UnaryExpr) Equal(other Expr) bool {
	o, ok := other.(*UnaryExpr)
	return ok && u != nil && o != nil && u.operator == o.operator && equalExpr(u.right, o.right)
}

func (u *UnaryExpr) Validate() error              { return validatePredicate(u) }
func (u *UnaryExpr) Accept(visitor Visitor) error { return accept(u, visitor) }
func (u *UnaryExpr) String() string               { return formatPredicate(u) }

func (u *UnaryExpr) Dispatch(onNot func(*UnaryExpr) error) error {
	if u == nil || u.operator != OpNot {
		return fmt.Errorf("filter: unsupported unary operator %q at %s", u.Operator(), u.Start())
	}
	if onNot == nil {
		return errors.New("filter: dispatch unary expression: NOT handler is nil")
	}
	return onNot(u)
}
