package tool

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

var (
	ErrDuplicateTool   = errors.New("tool: duplicate tool")
	ErrInvalidRegistry = errors.New("tool: invalid registry")
)

// Registry is concurrency-safe with a usable zero value. Registration is atomic
// for the full batch and snapshots definitions; model-visible views are detached
// and ordered by name.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]Binding
}

func NewRegistry(initial ...Binding) (*Registry, error) {
	registry := &Registry{}
	if err := registry.Register(initial...); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Register(values ...Binding) error {
	if r == nil {
		return ErrInvalidRegistry
	}
	if len(values) == 0 {
		return nil
	}

	pending := make(map[string]Binding, len(values))
	names := make([]string, 0, len(values))
	for index, binding := range values {
		if binding.contract.state == nil || binding.executable == nil {
			return fmt.Errorf("tools[%d]: %w", index, ErrInvalidTool)
		}
		name := binding.Contract().Name()
		if _, duplicate := pending[name]; duplicate {
			return fmt.Errorf("%w: %q appears more than once in batch", ErrDuplicateTool, name)
		}
		pending[name] = binding
		names = append(names, name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		if _, duplicate := r.entries[name]; duplicate {
			return fmt.Errorf("%w: %q is already registered", ErrDuplicateTool, name)
		}
	}
	if r.entries == nil {
		r.entries = make(map[string]Binding, len(pending))
	}
	maps.Copy(r.entries, pending)
	return nil
}

func (r *Registry) Resolve(name string) (Binding, bool) {
	if r == nil {
		return Binding{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.entries[name]
	return value, ok
}

func (r *Registry) Definitions() []Definition {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	definitions := make([]Definition, 0, len(r.entries))
	for _, binding := range r.entries {
		definitions = append(definitions, binding.Contract().Definition())
	}
	r.mu.RUnlock()

	slices.SortFunc(definitions, func(a, b Definition) int {
		return strings.Compare(a.Name, b.Name)
	})
	return definitions
}
