package mcp

import (
	"context"
	"crypto/rand"
	"fmt"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/httpclient"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

type ToolSource struct {
	Name string

	// Discovered tools borrow this initialized session. The caller must keep it
	// open for their calls and remains responsible for closing it.
	Session *sdkmcp.ClientSession
}

// Schedulers may rerun this callback during durable recovery and retain it
// beyond RPC sessions. It must be deterministic, side-effect-free, safe for
// concurrent use, and free of Tool, session, or execution-backend captures.
// Annotations are isolated and the invocation is schema-validated. Results follow
// [github.com/Tangerg/scope/agent/interaction.ConcurrentTool]; direct
// Tool.Call invocations remain the host's responsibility.
type ToolConcurrencyPolicy func(
	sourceName, remoteName string,
	annotations sdkmcp.ToolAnnotations,
	invocation toolcontract.Invocation,
) (key string, concurrent bool)

// The Host owns collision resolution across independent server catalogs.
type PublicToolNameFunc func(sourceName, remoteName string) string

// ToolEffectPolicy establishes a host-trusted replay contract. MCP annotations
// alone are untrusted hints; IdempotentHint is not durable key deduplication.
// This callback runs once during discovery and must not depend on mutable state.
type ToolEffectPolicy func(sourceName, remoteName string, annotations sdkmcp.ToolAnnotations) toolcontract.Effect

type ToolDiscoveryConfig struct {
	EffectPolicy ToolEffectPolicy
	// Each source and remote name is a separate escaped path segment. An
	// explicit override must remain unique; model aliasing belongs to the
	// binding that exposes this tool to a particular model.
	PublicName PublicToolNameFunc

	// If nil, no additional request metadata is forwarded to tool calls.
	RequestMeta RequestMetaFunc

	// If nil, tools provide no overlap declaration; this does not serialize calls.
	// Protocol descriptors cannot establish resource conflicts for the host.
	ConcurrencyPolicy ToolConcurrencyPolicy
}

func (t ToolDiscoveryConfig) publicName(sourceName, remoteName string) string {
	if t.PublicName != nil {
		return t.PublicName(sourceName, remoteName)
	}
	if sourceName == "" {
		return httpclient.PathSegment(remoteName)
	}
	return httpclient.PathSegment(sourceName) + "/" + httpclient.PathSegment(remoteName)
}

func DiscoverTools(ctx context.Context, sources []ToolSource, config ToolDiscoveryConfig) ([]toolcontract.Tool, error) {
	var tools []toolcontract.Tool
	seen := make(map[string]struct{})
	for sourceIndex, source := range sources {
		if source.Session == nil {
			return nil, fmt.Errorf("mcp: tool source %d %q: %w", sourceIndex, source.Name, ErrNilSession)
		}
		for descriptor, err := range source.Session.Tools(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("mcp: list tools from source %q: %w", source.Name, err)
			}
			if descriptor == nil {
				return nil, fmt.Errorf("mcp: source %q returned a nil tool descriptor", source.Name)
			}
			name := config.publicName(source.Name, descriptor.Name)
			snapshot, err := newDescriptorSnapshot(*descriptor, name)
			if err != nil {
				return nil, fmt.Errorf("mcp: snapshot tool from source %q: %w", source.Name, err)
			}
			if config.EffectPolicy != nil {
				snapshot.definition.Effect = config.EffectPolicy(source.Name, snapshot.remoteName, cloneToolAnnotations(snapshot.toolAnnotations))
				if !snapshot.definition.Effect.Valid() {
					return nil, fmt.Errorf("mcp: invalid effect declaration for %q", name)
				}
			}
			if _, exists := seen[name]; exists {
				return nil, fmt.Errorf("mcp: duplicate tool name %q after public naming", name)
			}
			seen[name] = struct{}{}
			tools = append(tools, remoteTool{
				binding:           rand.Text(),
				session:           source.Session,
				descriptor:        snapshot,
				requestMeta:       config.RequestMeta,
				sourceName:        source.Name,
				concurrencyPolicy: config.ConcurrencyPolicy,
			})
		}
	}
	return tools, nil
}
