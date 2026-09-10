package a2a

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2aclient"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

// ToolSet owns the clients behind an immutable view of remote-agent tools.
// Tools remain usable until Close; the value must not be copied after first use.
type ToolSet struct {
	clients   []*a2aclient.Client
	tools     []toolcontract.Tool
	closeOnce sync.Once
	closeErr  error
}

// OpenToolSet closes every client opened before a later endpoint fails, so a
// failed construction never transfers partial lifecycle ownership to callers.
func OpenToolSet(ctx context.Context, endpoints ...Endpoint) (*ToolSet, error) {
	toolSet := &ToolSet{}
	seen := make(map[string]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		client, card, err := dial(ctx, endpoint)
		if err != nil {
			return nil, errors.Join(err, toolSet.Close())
		}
		toolSet.clients = append(toolSet.clients, client)

		remote, err := newRemoteTool(remoteToolConfig{
			client: client, card: card, name: endpoint.Name,
			concurrencyPolicy: endpoint.ConcurrencyPolicy,
		})
		if err != nil {
			return nil, errors.Join(err, toolSet.Close())
		}
		name := remote.Definition().Name
		if _, dup := seen[name]; dup {
			err := fmt.Errorf("a2a: duplicate remote-agent tool name %q", name)
			return nil, errors.Join(err, toolSet.Close())
		}
		seen[name] = struct{}{}
		toolSet.tools = append(toolSet.tools, remote)
	}
	return toolSet, nil
}

// Tools returns a snapshot so callers cannot mutate the set's ordered view.
func (t *ToolSet) Tools() []toolcontract.Tool {
	if t == nil {
		return nil
	}
	return slices.Clone(t.tools)
}

// Close releases every remote-agent client in reverse acquisition order. It
// is nil-safe and idempotent so multiple shutdown paths can share the owner.
func (t *ToolSet) Close() error {
	if t == nil {
		return nil
	}
	t.closeOnce.Do(func() {
		var errs []error
		for _, client := range slices.Backward(t.clients) {
			if err := client.Destroy(); err != nil {
				errs = append(errs, err)
			}
		}
		t.closeErr = errors.Join(errs...)
	})
	return t.closeErr
}
