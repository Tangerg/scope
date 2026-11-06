package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/go-sdk/jsonrpc"
	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/mcp/internal/registration"
	"github.com/Tangerg/scope/otel/errortelemetry"
)

// Atomic admission prevents partially registered catalogs. Handlers retain
// the admitted identities because Tools may mutate after registration.
func Register(server *sdkmcp.Server, tools ...toolcontract.Binding) error {
	if server == nil {
		return ErrNilServer
	}
	registry, err := toolcontract.NewRegistry(tools...)
	if err != nil {
		return fmt.Errorf("mcp: register tools: %w", err)
	}
	var registrations []registration.Tool
	for _, definition := range registry.Definitions() {
		executable, _ := registry.Resolve(definition.Name)
		tool := serverTool{executable: executable}
		// The low-level AddTool keeps the Tool's own schema; the generic SDK
		// helper would replace it with a reflected one.
		descriptor := &sdkmcp.Tool{Name: definition.Name, Description: definition.Description, InputSchema: definition.InputSchema}
		if len(definition.OutputSchema) != 0 {
			descriptor.OutputSchema = definition.OutputSchema
		}
		// Served calls carry no idempotency key, so only a safe Tool's promise
		// survives this boundary; IdempotentHint would overstate it.
		if definition.Effect == toolcontract.EffectSafe {
			descriptor.Annotations = &sdkmcp.ToolAnnotations{ReadOnlyHint: true}
		}
		registrations = append(registrations, registration.Tool{Descriptor: descriptor, Handler: tool.handle})
	}
	return registration.Register(server, registrations...)
}

type serverTool struct {
	executable toolcontract.Binding
}

// Ordinary errors cannot prove a tool outcome, and diagnostics must not reach the model.
func (s serverTool) handle(ctx context.Context, req *sdkmcp.CallToolRequest) (out *sdkmcp.CallToolResult, err error) {
	toolName := s.executable.Contract().Name()
	ctx, span := mcpTracer.Start(ctx, "mcp.tool.serve "+toolName,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(semconv.GenAIToolName(toolName)),
	)
	defer func() {
		// The SDK invokes this callback on its own goroutine; an HTTP recovery
		// middleware cannot contain it or prevent private panic values escaping.
		if recover() != nil {
			out, err = s.callError(errToolCallbackPanic)
		}
	}()
	var callErr error
	defer errortelemetry.Finish(&callErr, func(observed error) {
		if observed != nil {
			errortelemetry.Record(span, errortelemetry.Classify(observed))
		}
		span.End()
	})
	ctx = toolcontract.WithProgressReporter(ctx, requestProgress(req))

	var rawArgs string
	if req != nil && req.Params != nil {
		if req.Params.InputResponses != nil || req.Params.RequestState != "" {
			// This direct binding never issued a server-side continuation. Treating
			// a retry as a fresh call could repeat an external write.
			callErr = &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "no continuation was issued for this tool binding"}
			return nil, callErr
		}
		rawArgs = string(req.Params.Arguments)
	}

	invocation, callErr := s.executable.Contract().Prepare(toolcontract.Request{Name: toolName, Arguments: rawArgs}, toolcontract.Admission{})
	if callErr != nil {
		return s.prepareError(callErr)
	}
	output, callErr := s.executable.Call(ctx, invocation)
	if callErr != nil {
		return s.callError(callErr)
	}
	if output.InputRequest != nil {
		callErr = errors.New("mcp: Tool requested external input; the server call has no continuation binding")
		return s.callError(callErr)
	}
	result, callErr := EncodeToolResult(output.Output)
	if callErr != nil {
		return s.callError(callErr)
	}
	return result, nil
}

func (s serverTool) prepareError(err error) (*sdkmcp.CallToolResult, error) {
	rejection, public := toolcontract.ClassifyInputRejection(err)
	if !public {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "tool input validation failed"}
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: rejection.Message()}},
		IsError: true,
	}, nil
}

func (s serverTool) callError(err error) (*sdkmcp.CallToolResult, error) {
	protocolError := &jsonrpc.Error{
		Code: jsonrpc.CodeInternalError, Message: "tool call did not produce a valid result",
	}
	failure, found := toolcontract.ClassifyFailure(err)
	if !found {
		return nil, protocolError
	}
	result, err := EncodeToolResult(failure.Output())
	if err != nil {
		return nil, protocolError
	}
	result.IsError = true
	return result, nil
}

// EncodeToolResult projects a validated Scope tool output onto its MCP wire response.
func EncodeToolResult(output toolcontract.Output) (*sdkmcp.CallToolResult, error) {
	if err := output.Validate(); err != nil {
		return nil, fmt.Errorf("mcp: invalid Tool output: %w", err)
	}
	result := &sdkmcp.CallToolResult{Content: make([]sdkmcp.Content, 0, len(output.Content))}
	for index := range output.Content {
		content, err := mapServerContent(output.Content[index])
		if err != nil {
			return nil, fmt.Errorf("mcp: tool output content[%d]: %w", index, err)
		}
		result.Content = append(result.Content, content)
	}
	if len(output.Details) != 0 {
		result.StructuredContent = slices.Clone(output.Details)
	}
	return result, nil
}
