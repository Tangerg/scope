package tool

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrDuplicateTool = errors.New("tool: duplicate tool")

// Registry is an immutable catalog of uniquely named bound Tools; its zero
// value is empty. Model-visible views are detached and ordered by name.
type Registry struct {
	entries map[string]Binding
}

// NewRegistry admits the whole catalog at once, so a rejected Binding leaves no
// partially built Registry behind.
func NewRegistry(bindings ...Binding) (Registry, error) {
	entries := make(map[string]Binding, len(bindings))
	for index, binding := range bindings {
		if !binding.bound() {
			return Registry{}, fmt.Errorf("tools[%d]: %w", index, ErrInvalidTool)
		}
		name := binding.Contract().Name()
		if _, duplicate := entries[name]; duplicate {
			return Registry{}, fmt.Errorf("%w: %q appears more than once", ErrDuplicateTool, name)
		}
		entries[name] = binding
	}
	return Registry{entries: entries}, nil
}

func (r Registry) Resolve(name string) (Binding, bool) {
	binding, found := r.entries[name]
	return binding, found
}

func (r Registry) Definitions() []Definition {
	definitions := make([]Definition, 0, len(r.entries))
	for _, binding := range r.entries {
		definitions = append(definitions, binding.Contract().Definition())
	}
	slices.SortFunc(definitions, func(a, b Definition) int {
		return strings.Compare(a.Name, b.Name)
	})
	return definitions
}
