package keyword

import (
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// EqualString returns the term for a top-level scalar string equality.
// Predicates without this exact representation return vectorstore.ErrUnsupportedFilter.
func EqualString(p filter.Predicate) (string, error) {
	if p == nil {
		return "", vectorstore.ErrMissingFilter
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	b, ok := p.(*filter.BinaryExpr)
	if !ok || b.Operator() != filter.OpEqual {
		return "", vectorstore.ErrUnsupportedFilter
	}
	path, err := b.Path()
	if err != nil || len(path) != 1 {
		return "", vectorstore.ErrUnsupportedFilter
	}
	key, ok := path[0].Key()
	if !ok {
		return "", vectorstore.ErrUnsupportedFilter
	}
	v, err := b.Value()
	if err != nil {
		return "", err
	}
	str, ok := v.(string)
	if !ok {
		return "", vectorstore.ErrUnsupportedFilter
	}
	return String(key, str), nil
}
