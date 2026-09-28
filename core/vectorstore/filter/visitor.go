package filter

import (
	"errors"

	"github.com/samber/lo"
)

type Visitor interface {
	// Visit consumes one complete, already validated predicate. Implementations
	// own traversal and may stop at the first target-specific error; they must not
	// mutate the immutable expression tree.
	Visit(predicate Predicate) error
}

func accept(predicate Predicate, visitor Visitor) error {
	if lo.IsNil(predicate) {
		return errors.New("filter: accept visitor: predicate is nil")
	}
	if err := predicate.Validate(); err != nil {
		return err
	}
	if lo.IsNil(visitor) {
		return errors.New("filter: accept visitor: visitor is nil")
	}
	return visitor.Visit(predicate)
}
