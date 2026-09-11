package tool

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/Tangerg/scope/core/chat"
)

var (
	ErrDuplicateTool   = errors.New("tool: duplicate tool")
	ErrInvalidRegistry = errors.New("tool: invalid registry")
)

// Registry is an instance-scoped, concurrency-safe collection of executable
// tools. Its zero value is ready to use. Each runtime or process owns its
// registry explicitly; there is no package-global counterpart. Registration is
// atomic for the full batch, definitions are snapshotted at registration time,
// and model-visible views are returned as defensive copies in stable name order.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]Binding
}

// NewRegistry is a convenience over the usable zero value for the common case
// of a fixed tool set. Registration is all-or-nothing, so a duplicate name in
// the initial batch leaves no partially populated registry behind.
func NewRegistry(initial ...Tool) (*Registry, error) {
	registry := &Registry{}
	if err := registry.Register(initial...); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Register(values ...Tool) error {
	if r == nil {
		return ErrInvalidRegistry
	}
	if len(values) == 0 {
		return nil
	}

	pending := make(map[string]Binding, len(values))
	for index, value := range values {
		binding, err := Bind(value)
		if err != nil {
			return fmt.Errorf("tools[%d]: %w", index, err)
		}
		definition := binding.Contract().Definition()
		if _, duplicate := pending[definition.Name]; duplicate {
			return fmt.Errorf("%w: %q appears more than once in batch", ErrDuplicateTool, definition.Name)
		}
		pending[definition.Name] = binding
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range pending {
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

func (r *Registry) Definitions() []chat.ToolDefinition {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	definitions := make([]chat.ToolDefinition, 0, len(r.entries))
	for _, binding := range r.entries {
		definitions = append(definitions, binding.Contract().Definition())
	}
	r.mu.RUnlock()

	slices.SortFunc(definitions, func(a, b chat.ToolDefinition) int {
		return strings.Compare(a.Name, b.Name)
	})
	return definitions
}
