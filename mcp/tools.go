package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

// ToolSource binds an initialized MCP client session to a logical name used to
// deconflict tools across multiple servers.
type ToolSource struct {
	// Name identifies the upstream server in tool prefixes and error
	// messages. Empty is allowed but discouraged when more than one
	// source is in play.
	Name string

	// Session is a live, initialized client session. The wrapper does not own
	// the session; callers are responsible for closing it.
	Session *sdkmcp.ClientSession
}

// ToolConcurrencyPolicy decides whether one remote tool call may overlap other calls
// from the same model response. A false result keeps the call exclusive; a true
// result with an empty key declares no known conflict, while equal non-empty
// keys serialize.
//
// The callback receives the source and remote tool names, an isolated copy of
// the remote annotations, and a schema-validated invocation. It must be deterministic,
// side-effect-free, and safe for concurrent use because a durable resume may
// plan queued calls again and callers may inspect the capability from multiple
// goroutines.
// It must not capture a Tool, session, or execution backend; schedulers retain
// the declaration independently of the remote tool's execution lifetime.
type ToolConcurrencyPolicy func(
	sourceName, remoteName string,
	annotations sdkmcp.ToolAnnotations,
	invocation toolcontract.Invocation,
) (key string, concurrent bool)

// PublicToolNameFunc maps a remote tool identity to the name a model sees.
// Remote servers choose names independently, so two sources can collide or emit
// characters providers reject; projecting the name here keeps that negotiation
// out of the tool contract and lets a host resolve collisions its own way.
type PublicToolNameFunc func(sourceName, remoteName string) string

const maxPublicToolNameLength = 64

// ToolDiscoveryConfig controls the boundary projection performed by
// [DiscoverTools].
type ToolDiscoveryConfig struct {
	// PublicName maps each remote tool identity to its public name. Nil
	// uses the package default, "<sourceName>_<remoteName>" sanitized to the
	// function-name charset accepted by model providers.
	PublicName PublicToolNameFunc

	// RequestMeta is applied to every tool produced. Nil forwards no metadata on
	// tool calls.
	RequestMeta RequestMetaFunc

	// ConcurrencyPolicy opts remote tools into a caller-owned scheduling policy. Nil
	// keeps every MCP call exclusive because protocol descriptors do not provide
	// a trustworthy resource-conflict contract. Callers retain ownership of
	// execution and result ordering. [AnnotatedReadOnlyConcurrencyPolicy] is the
	// conservative ready-made policy for trusted descriptors that declare
	// readOnlyHint=true.
	ConcurrencyPolicy ToolConcurrencyPolicy
}

// publicName returns the configured public name or a provider-safe default.
// MCP itself permits names that model providers reject, while calls still need
// to route by the unchanged raw MCP name.
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

			remote, err := newRemoteTool(remoteToolConfig{
				source:            source,
				descriptor:        snapshot,
				requestMeta:       config.RequestMeta,
				concurrencyPolicy: config.ConcurrencyPolicy,
			})
			if err != nil {
				return nil, fmt.Errorf("mcp: wrap tool %q from source %q: %w", snapshot.remoteName, source.Name, err)
			}

			if _, exists := seen[name]; exists {
				return nil, fmt.Errorf("mcp: duplicate tool name %q after public naming", name)
			}
			seen[name] = struct{}{}
			tools = append(tools, remote)
		}
	}
	return tools, nil
}
