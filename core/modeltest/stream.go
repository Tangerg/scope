package modeltest

import (
	"iter"
)

// Collect stops at the first error and returns values yielded before it.
func Collect[T any](seq iter.Seq2[T, error]) ([]T, error) {
	var out []T
	for v, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, v)
	}
	return out, nil
}

// CollectN returns at most count values, stopping on the first error.
// A nonpositive count does not start the iterator.
func CollectN[T any](sequence iter.Seq2[T, error], count int) ([]T, error) {
	if count <= 0 {
		return nil, nil
	}
	var values []T
	for value, err := range sequence {
		if err != nil {
			return values, err
		}
		values = append(values, value)
		if len(values) >= count {
			return values, nil
		}
	}
	return values, nil
}
