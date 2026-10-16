package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

type ToolSource struct {
	Name string

	// The caller owns the session lifetime.
	Session *sdkmcp.ClientSession
}

// Schedulers may rerun this callback during durable recovery and retain it
// beyond RPC sessions. It must be deterministic, side-effect-free, safe for
// concurrent use, and free of Tool, session, or execution-backend captures.
// Annotations are isolated and the invocation is schema-validated. Results follow
// [github.com/Tangerg/scope/agent/strategy/interaction.ConcurrentTool]; direct
// Tool.Call invocations remain the host's responsibility.
type ToolConcurrencyPolicy func(
	sourceName, remoteName string,
	annotations sdkmcp.ToolAnnotations,
	invocation toolcontract.Invocation,
) (key string, concurrent bool)

// The Host owns collision resolution across independent server catalogs.
type PublicToolNameFunc func(sourceName, remoteName string) string

type ToolDiscoveryConfig struct {
	PublicName PublicToolNameFunc

	RequestMeta RequestMetaFunc

	// Protocol descriptors cannot establish resource conflicts for the host.
	ConcurrencyPolicy ToolConcurrencyPolicy
}

func (t ToolDiscoveryConfig) publicName(sourceName, remoteName string) string {
	if t.PublicName != nil {
		return t.PublicName(sourceName, remoteName)
	}
	if sourceName == "" {
		return chat.SanitizeToolName(remoteName)
	}
	return chat.SanitizeToolName(sourceName + "_" + remoteName)
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
			if _, exists := seen[name]; exists {
				return nil, fmt.Errorf("mcp: duplicate tool name %q after public naming", name)
			}
			seen[name] = struct{}{}
			tools = append(tools, remoteTool{
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
