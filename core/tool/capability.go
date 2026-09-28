package tool

import (
	"errors"
	"fmt"

	"github.com/samber/lo"
)

var ErrInvalidWrappingChain = errors.New("tool: invalid wrapping chain")

// WrappingTool exposes the decorator chain used to resolve optional capabilities.
type WrappingTool interface {
	// Unwrap returns the next inner tool in a finite decorator chain. It must
	// return the same tool for the wrapper's lifetime and must not perform I/O;
	// cycles and excessive depth make the chain invalid.
	Unwrap() Tool
}

const maxWrappingDepth = 64

// Capability finds T on value or through its wrapping chain. The outermost
// implementation wins. Malformed chains are returned as errors.
func Capability[T any](value Tool) (capability T, found bool, err error) {
	current := value
	for depth := 0; !lo.IsNil(current); depth++ {
		if capability, ok := any(current).(T); ok {
			return capability, true, nil
		}
		wrapper, ok := current.(WrappingTool)
		if !ok {
			break
		}
		if depth == maxWrappingDepth {
			var zero T
			return zero, false, fmt.Errorf(
				"%w: %T exceeds %d wrappers",
				ErrInvalidWrappingChain,
				value,
				maxWrappingDepth,
			)
		}
		current = wrapper.Unwrap()
	}
	var zero T
	return zero, false, nil
}
