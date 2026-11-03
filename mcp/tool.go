package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/core/jsonschema"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/core/metadata"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/otel/errortelemetry"
)

type remoteTool struct {
	binding           string
	session           *sdkmcp.ClientSession
	descriptor        descriptorSnapshot
	requestMeta       RequestMetaFunc
	sourceName        string
	concurrencyPolicy ToolConcurrencyPolicy
}

var _ toolcontract.Tool = remoteTool{}

func (r remoteTool) Definition() toolcontract.Definition { return r.descriptor.definition.Clone() }

// Sanitized provider names can collide; policy uses the original identity.
func (r remoteTool) MCPToolIdentity() (sourceName, remoteName string) {
	return r.sourceName, r.descriptor.remoteName
}

func (r remoteTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	if r.concurrencyPolicy == nil {
		return nil
	}
	policy, sourceName, remoteName := r.concurrencyPolicy, r.sourceName, r.descriptor.remoteName
	annotations := r.descriptor.toolAnnotations
	return func(invocation toolcontract.Invocation) (string, bool) {
		return policy(sourceName, remoteName, cloneToolAnnotations(annotations), invocation)
	}
}

func (r remoteTool) Call(ctx context.Context, invocation toolcontract.Invocation) (out toolcontract.Result, err error) {
	remoteName := r.descriptor.remoteName
	ctx, span := mcpTracer.Start(ctx, "mcp.tool.call "+remoteName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.GenAIToolName(remoteName)),
	)
	defer func() {
		if err != nil {
			errortelemetry.Record(span, errortelemetry.Classify(err))
		}
		span.End()
	}()

	params := &sdkmcp.CallToolParams{
		Name:      remoteName,
		Arguments: json.RawMessage(invocation.Arguments()),
	}
	if r.requestMeta != nil {
		params.Meta = sdkmcp.Meta(metadata.Map(r.requestMeta(ctx)).Clone())
	}

	if c, ok := invocation.Continuation(); ok {
		if resumeErr := r.resume(params, invocation, c); resumeErr != nil {
			return toolcontract.Result{}, resumeErr
		}
	}
	res, err := r.session.CallTool(ctx, params)
	if err != nil {
		return toolcontract.Result{}, fmt.Errorf("mcp: call tool %q: %w", remoteName, err)
	}
	if res != nil && res.NeedsInput() {
		return r.input(invocation, res)
	}
	output, err := remoteResult{remoteName: remoteName, value: res}.unwrap()
	return toolcontract.Result{Output: output}, err
}

// A remote checkpoint belongs to the exact admitted binding and original
// arguments. Reconnecting or rediscovering a tool cannot silently start it over.
type remoteCheckpoint struct {
	Binding   string            `json:"binding"`
	Args      [sha256.Size]byte `json:"args"`
	RequestID string            `json:"request_id"`
	State     string            `json:"state"`
}

func (r remoteTool) input(inv toolcontract.Invocation, result *sdkmcp.CallToolResult) (toolcontract.Result, error) {
	if init := r.session.InitializeResult(); init == nil || init.ProtocolVersion < "2026-07-28" || len(result.InputRequests) != 1 {
		return toolcontract.Result{}, ErrIncompleteResult
	}
	for id, req := range result.InputRequests {
		form, ok := req.(*sdkmcp.ElicitParams)
		if !ok || form == nil || id == "" || form.Message == "" || form.URL != "" || form.ElicitationID != "" || (form.Mode != "" && form.Mode != "form") || form.RequestedSchema == nil {
			return toolcontract.Result{}, ErrIncompleteResult
		}
		// Keep decline/cancel distinct from an affirmative answer. The form is
		// an independent MCP schema; do not embed schemas with root-local references.
		encoded, err := jsonv2.Marshal(form.RequestedSchema)
		if err != nil {
			return toolcontract.Result{}, err
		}
		var schemaObject map[string]any
		if err = jsonv2.Unmarshal(encoded, &schemaObject); err != nil || schemaObject["type"] != "object" || relocatesSchema(schemaObject) {
			return toolcontract.Result{}, ErrIncompleteResult
		}
		answer := map[string]any{"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"action": map[string]string{"const": "accept"}, "content": json.RawMessage(encoded)}, "required": []string{"action", "content"}, "additionalProperties": false},
			map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"enum": []string{"decline", "cancel"}}}, "required": []string{"action"}, "additionalProperties": false},
		}}
		data, err := jsonv2.Marshal(answer)
		if err != nil {
			return toolcontract.Result{}, err
		}
		schema, err := jsonschema.Parse(data)
		if err != nil {
			return toolcontract.Result{}, err
		}
		state, err := jsonv2.Marshal(remoteCheckpoint{Binding: r.binding, Args: sha256.Sum256([]byte(inv.Arguments())), RequestID: id, State: result.RequestState})
		if err != nil {
			return toolcontract.Result{}, err
		}
		prompt, err := jsonv2.Marshal(form.Message)
		if err != nil {
			return toolcontract.Result{}, err
		}
		request, err := toolcontract.NewInputRequest(toolcontract.InputRequestConfig{Prompt: prompt, ResponseSchema: schema, ContinuationState: state})
		if err != nil {
			return toolcontract.Result{}, err
		}
		return toolcontract.Result{InputRequest: &request}, nil
	}
	return toolcontract.Result{}, ErrIncompleteResult
}

func relocatesSchema(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for k, child := range v {
			switch k {
			case "$ref", "$dynamicRef", "$id", "$schema":
				return true
			}
			if relocatesSchema(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if relocatesSchema(child) {
				return true
			}
		}
	}
	return false
}

func (r remoteTool) resume(params *sdkmcp.CallToolParams, inv toolcontract.Invocation, c toolcontract.Continuation) error {
	var s remoteCheckpoint
	if err := jsonv2.Unmarshal(c.State(), &s, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: malformed remote checkpoint", ErrContinuationBinding)
	}
	if s.Binding != r.binding || s.RequestID == "" || s.Args != sha256.Sum256([]byte(inv.Arguments())) {
		return ErrContinuationBinding
	}
	var a struct {
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"`
	}
	if err := jsonv2.Unmarshal(c.Response(), &a, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	response := &sdkmcp.ElicitResult{Action: a.Action}
	switch a.Action {
	case "accept":
		if a.Content == nil {
			return ErrContinuationBinding
		}
		response.Content = make(map[string]any, len(a.Content))
		for k, v := range a.Content {
			response.Content[k] = v
		}
	case "decline", "cancel":
		if a.Content != nil {
			return ErrContinuationBinding
		}
	default:
		return ErrContinuationBinding
	}
	params.InputResponses = sdkmcp.InputResponseMap{s.RequestID: response}
	params.RequestState = s.State
	return nil
}
