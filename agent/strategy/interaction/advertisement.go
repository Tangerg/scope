package interaction

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

var (
	ErrToolAdvertisementUnavailable = errors.New("interaction: tool advertisement unavailable")
	ErrInvalidToolAdvertisement     = errors.New("interaction: invalid tool advertisement")
)

type toolAdvertisementContextKey struct{}

// AdvertiseTools stages already-bound deferred Tools for model visibility from
// the next model call onward. The change commits only if the current Tool call
// succeeds. It never adds executable authority. Names must be exact deferred
// Tool names; repeated names are idempotent.
// A nil ctx is invalid and panics; a context without an active Tool call returns
// ErrToolAdvertisementUnavailable.
func AdvertiseTools(ctx context.Context, names ...string) error {
	if ctx == nil {
		panic(errors.New("interaction: nil Context"))
	}
	advertiser, present := ctx.Value(toolAdvertisementContextKey{}).(*toolAdvertiser)
	if !present || advertiser == nil {
		return ErrToolAdvertisementUnavailable
	}
	return advertiser.advertise(names)
}

type toolAdvertiser struct {
	mu      sync.Mutex
	allowed map[string]struct{}
	seen    map[string]struct{}
	names   []string
}

func newToolAdvertiser(allowed map[string]struct{}) *toolAdvertiser {
	return &toolAdvertiser{
		allowed: allowed,
		seen:    make(map[string]struct{}),
	}
}

func (t *toolAdvertiser) advertise(names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("%w: at least one Tool name is required", ErrInvalidToolAdvertisement)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, name := range names {
		if name == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("%w: tool name %q is empty or has surrounding whitespace", ErrInvalidToolAdvertisement, name)
		}
		if _, allowed := t.allowed[name]; !allowed {
			return fmt.Errorf("%w: tool %q is not deferred", ErrInvalidToolAdvertisement, name)
		}
	}
	for _, name := range names {
		if _, duplicate := t.seen[name]; duplicate {
			continue
		}
		t.seen[name] = struct{}{}
		t.names = append(t.names, name)
	}
	return nil
}

func (t *toolAdvertiser) advertisedNames() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.names)
}

func withToolAdvertiser(ctx context.Context, advertiser *toolAdvertiser) context.Context {
	return context.WithValue(ctx, toolAdvertisementContextKey{}, advertiser)
}

func validateAdvertisedToolNames(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for index, name := range names {
		if name == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("advertised Tool name %d is empty or has surrounding whitespace", index)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("advertised Tool name %q is duplicated", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}
