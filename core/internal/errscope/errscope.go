// Package errscope owns the single-scope rule Core's outcome classifiers share.
// Transparent wrapping keeps one scope, so its cause may still speak for the
// enclosing call. A join of independent errors has no single cause that may
// speak for it, so the search stops there instead of letting either child
// decide the outcome, as errors.As would.
package errscope

import "github.com/samber/lo"

// Find returns the first T in err's single-scope chain.
//
//nolint:errorlint // errors.As would cross independent scopes at a join.
func Find[T error](err error) (T, bool) {
	for !lo.IsNil(err) {
		if target, found := err.(T); found {
			return target, true
		}
		err = cause(err)
	}
	var zero T
	return zero, false
}

//nolint:errorlint // Each node owns one scope; see Find.
func cause(err error) error {
	switch wrapped := err.(type) {
	case interface{ Unwrap() error }:
		return wrapped.Unwrap()
	case interface{ Unwrap() []error }:
		var single error
		for _, candidate := range wrapped.Unwrap() {
			if candidate == nil {
				continue
			}
			if single != nil {
				return nil
			}
			single = candidate
		}
		return single
	default:
		return nil
	}
}
