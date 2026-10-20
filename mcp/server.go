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

	corechat "github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// Atomic admission prevents partially registered catalogs. Handlers retain
// the admitted identities because Tools may mutate after registration.
func Register(server *sdkmcp.Server, tools ...toolcontract.Tool) error {
	if server == nil {
		return ErrNilServer
	}
	registry, err := toolcontract.NewRegistry(tools...)
	if err != nil {
		return fmt.Errorf("mcp: register tools: %w", err)
	}
	for _, definition := range registry.Definitions() {
		executable, _ := registry.Resolve(definition.Name)
		tool := serverTool{executable: executable, name: definition.Name}
		// The low-level AddTool keeps the Tool's own schema; the generic SDK
		// helper would replace it with a reflected one.
		server.AddTool(new(sdkmcp.Tool{
			Name: definition.Name, Description: definition.Description, InputSchema: definition.InputSchema,
		}), tool.handle)
	}
	return nil
}

type serverTool struct {
	executable toolcontract.Binding
	name       string
}

// Ordinary errors cannot prove a tool outcome, and diagnostics must not reach the model.
func (s serverTool) handle(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	toolName := s.name
	ctx, span := mcpTracer.Start(ctx, "mcp.tool.serve "+toolName,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(semconv.GenAIToolName(toolName)),
	)
	defer span.End()
	ctx = withServerCall(ctx, req)

	var rawArgs string
	if req != nil && req.Params != nil {
		rawArgs = string(req.Params.Arguments)
	}

	invocation, err := s.executable.Contract().Prepare(corechat.ToolCall{
		ID: "mcp/" + toolName, Name: toolName, Arguments: rawArgs,
	}, nil)
	if err != nil {
		return s.prepareError(span, err)
	}
	output, err := s.executable.Call(ctx, invocation)
	if err != nil {
		return s.callError(span, err)
	}
	if output.InputRequest != nil {
		return s.callError(span, errors.New("mcp: Tool requested external input; the server call has no continuation binding"))
	}
	result, err := mapServerToolOutput(output.Output)
	if err != nil {
		return s.callError(span, err)
	}
	return result, nil
}

// Validator panics are host defects, even when they wrap a tool Failure.
func (s serverTool) prepareError(span trace.Span, err error) (*sdkmcp.CallToolResult, error) {
	recordSpanError(span, err)
	if _, defect := errors.AsType[*toolcontract.InputValidationPanicError](err); defect {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "tool input validation failed"}
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
		IsError: true,
	}, nil
}

func (s serverTool) callError(span trace.Span, err error) (*sdkmcp.CallToolResult, error) {
	recordSpanError(span, err)
	protocolError := &jsonrpc.Error{
		Code: jsonrpc.CodeInternalError, Message: "tool call did not produce a valid result",
	}
	failure, found := errors.AsType[*toolcontract.Failure](err)
	if !found {
		return nil, protocolError
	}
	if validationErr := failure.Validate(); validationErr != nil {
		recordSpanError(span, validationErr)
		return nil, protocolError
	}
	result, err := mapServerToolOutput(failure.Output())
	if err != nil {
		recordSpanError(span, err)
		return nil, protocolError
	}
	result.IsError = true
	return result, nil
}

func mapServerToolOutput(output corechat.ToolOutput) (*sdkmcp.CallToolResult, error) {
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
