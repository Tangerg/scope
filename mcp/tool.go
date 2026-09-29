package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	corechat "github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

type remoteTool struct {
	session           *sdkmcp.ClientSession
	descriptor        descriptorSnapshot
	requestMeta       RequestMetaFunc
	sourceName        string
	concurrencyPolicy ToolConcurrencyPolicy
}

var _ toolcontract.Tool = remoteTool{}

func (r remoteTool) Definition() corechat.ToolDefinition { return r.descriptor.definition.Clone() }

// MCPToolIdentity returns the unsanitized source and remote tool names bound to
// this wrapper. Consumers use the pair for policy decisions; Definition.Name is
// a provider-constrained presentation label and is not an injective identity.
func (r remoteTool) MCPToolIdentity() (sourceName, remoteName string) {
	return r.sourceName, r.descriptor.remoteName
}

// ConcurrencyPolicy freezes scheduling data without retaining the RPC session.
// Unknown tools remain exclusive unless discovery received a host policy.
func (r remoteTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	if r.concurrencyPolicy == nil {
		return nil
	}
	policy, sourceName, remoteName := r.concurrencyPolicy, r.sourceName, r.descriptor.remoteName
	annotations := r.descriptor.annotations()
	return func(invocation toolcontract.Invocation) (string, bool) {
		return policy(sourceName, remoteName, cloneToolAnnotations(annotations), invocation)
	}
}

func (r remoteTool) Call(ctx context.Context, invocation toolcontract.Invocation) (out corechat.ToolOutput, err error) {
	remoteName := r.descriptor.remoteName
	ctx, span := mcpTracer.Start(ctx, "mcp.tool.call "+remoteName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.GenAIToolName(remoteName)),
	)
	defer func() {
		if err != nil {
			recordSpanError(span, err)
		}
		span.End()
	}()

	params := &sdkmcp.CallToolParams{
		Name:      remoteName,
		Arguments: json.RawMessage(invocation.Arguments()),
	}
	if r.requestMeta != nil {
		if meta := r.requestMeta(ctx); len(meta) > 0 {
			params.Meta = maps.Clone(meta)
		}
	}

	res, err := r.session.CallTool(ctx, params)
	if err != nil {
		return corechat.ToolOutput{}, fmt.Errorf("mcp: call tool %q: %w", remoteName, err)
	}
	return remoteResult{remoteName: remoteName, value: res, outputSchema: r.descriptor.outputSchema}.unwrap()
}
