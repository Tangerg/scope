package keyword

import (
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// EqualString returns the term for a top-level scalar string equality.
// Predicates without this exact representation return vectorstore.ErrUnsupportedFilter.
func EqualString(p filter.Predicate) (string, error) {
	if lo.IsNil(p) {
		return "", vectorstore.ErrMissingFilter
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	b, ok := p.(*filter.BinaryExpr)
	if !ok || b.Operator() != filter.OpEqual {
		return "", vectorstore.ErrUnsupportedFilter
	}
	condition, ok, err := Lower(b)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", vectorstore.ErrUnsupportedFilter
	}
	return condition.Term, nil
}

// Condition is a membership test over the terms [Project] derives: a document
// satisfies it when its terms contain Term, or lack it when Negated.
type Condition struct {
	Term    string
	Negated bool
}

// Lower returns the Condition exactly equivalent to a comparison of one
// top-level member: equality or inequality with a string, or IS NULL. It
// reports false for a comparison without such a Condition.
func Lower(b *filter.BinaryExpr) (Condition, bool, error) {
	path, err := b.Path()
	if err != nil || len(path) != 1 {
		return Condition{}, false, nil
	}
	key, ok := path[0].Key()
	if !ok {
		return Condition{}, false, nil
	}
	switch b.Operator() {
	case filter.OpIs:
		return Condition{Term: Presence(key), Negated: true}, true, nil
	case filter.OpEqual, filter.OpNotEqual:
		v, err := b.Value()
		if err != nil {
			return Condition{}, false, err
		}
		str, ok := v.(string)
		if !ok {
			return Condition{}, false, nil
		}
		return Condition{Term: String(key, str), Negated: b.Operator() == filter.OpNotEqual}, true, nil
	default:
		return Condition{}, false, nil
	}
}
