package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

// ToolSource is one live client session whose tools DiscoverTools projects.
type ToolSource struct {
	// Name prefixes public tool names and errors. Empty is allowed.
	Name string

	// Session must be initialized. Discovered tools borrow it; the caller
	// closes it.
	Session *sdkmcp.ClientSession
}

// ToolConcurrencyPolicy decides whether a remote tool call may overlap other
// calls from the same model response. False keeps the call exclusive; true with
// an empty key declares no known conflict, while equal non-empty keys serialize.
//
// It receives an isolated copy of the annotations and a schema-validated
// invocation. It must be deterministic, side-effect-free, and safe for
// concurrent use, because a durable resume may plan queued calls again. It must
// not capture a Tool, session, or execution backend: schedulers retain it
// beyond the remote tool's execution lifetime.
type ToolConcurrencyPolicy func(
	sourceName, remoteName string,
	annotations sdkmcp.ToolAnnotations,
	invocation toolcontract.Invocation,
) (key string, concurrent bool)

// PublicToolNameFunc projects remote identities into model-compatible names.
// The Host owns collision resolution across independent server catalogs.
type PublicToolNameFunc func(sourceName, remoteName string) string

const maxPublicToolNameLength = 64

// ToolDiscoveryConfig configures how DiscoverTools projects remote tools.
type ToolDiscoveryConfig struct {
	// PublicName nil uses "<sourceName>_<remoteName>" sanitized to the
	// function-name charset model providers accept, truncated to 64 bytes.
	PublicName PublicToolNameFunc

	// RequestMeta nil forwards no metadata on tool calls.
	RequestMeta RequestMetaFunc

	// ConcurrencyPolicy nil keeps every MCP call exclusive, because protocol
	// descriptors carry no trustworthy resource-conflict contract.
	ConcurrencyPolicy ToolConcurrencyPolicy
}

func (t ToolDiscoveryConfig) publicName(sourceName, remoteName string) string {
	if t.PublicName != nil {
		return t.PublicName(sourceName, remoteName)
	}
	if sourceName == "" {
		return sanitizeToolName(remoteName)
	}
	return sanitizeToolName(sourceName + "_" + remoteName)
}

func sanitizeToolName(name string) string {
	sanitized := make([]byte, 0, len(name))
	for i := range len(name) {
		character := name[i]
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '_', character == '-':
			sanitized = append(sanitized, character)
		default:
			sanitized = append(sanitized, '_')
		}
	}
	return string(sanitized[:min(len(sanitized), maxPublicToolNameLength)])
}

// DiscoverTools reads each live session's current catalog and projects every
// remote descriptor into a Scope tool. It does not cache or own the sessions.
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
