package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
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

type remoteToolConfig struct {
	source            ToolSource
	descriptor        descriptorSnapshot
	requestMeta       RequestMetaFunc
	concurrencyPolicy ToolConcurrencyPolicy
}

func newRemoteTool(config remoteToolConfig) (remoteTool, error) {
	if config.source.Session == nil {
		return remoteTool{}, ErrNilSession
	}
	return remoteTool{
		session:           config.source.Session,
		descriptor:        config.descriptor,
		requestMeta:       config.requestMeta,
		sourceName:        config.source.Name,
		concurrencyPolicy: config.concurrencyPolicy,
	}, nil
}

func (r remoteTool) Definition() corechat.ToolDefinition { return r.descriptor.definition.Clone() }

// MCPToolIdentity returns the unsanitized source and remote tool names bound to
// this wrapper. Consumers use the pair for policy decisions; Definition.Name is
// a provider-constrained presentation label and is not an injective identity.
func (r remoteTool) MCPToolIdentity() (sourceName, remoteName string) {
	return r.sourceName, r.descriptor.remoteName
}

// ConcurrencyKey structurally satisfies schedulers that support conflict-aware
// parallel calls without coupling this protocol adapter to a particular agent
// runtime. Unknown remote tools remain exclusive unless the caller supplied a
// policy through [ToolDiscoveryConfig.ConcurrencyPolicy].
func (r remoteTool) ConcurrencyKey(invocation toolcontract.Invocation) (key string, concurrent bool) {
	if r.concurrencyPolicy == nil {
		return "", false
	}
	return r.concurrencyPolicy(r.sourceName, r.descriptor.remoteName, r.descriptor.annotations(), invocation)
}

// A remote IsError result becomes [tool.Failure], preserving its complete
// content separately from transport and protocol errors.
//
// One `mcp.tool.call <name>` span per call (kind=Client), carrying
// `gen_ai.tool.name`; a failed call records the error and sets the span
// status to Error (no separate bool attribute). No-op overhead when no
// TracerProvider is configured.
func (r remoteTool) Call(ctx context.Context, invocation toolcontract.Invocation) (out corechat.ToolOutput, err error) {
	remoteName := r.descriptor.remoteName
	ctx, span := mcpTracer.Start(ctx, "mcp.tool.call "+remoteName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String(attrToolName, remoteName)),
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
	return remoteResult{remoteName: remoteName, value: res}.unwrap()
}
