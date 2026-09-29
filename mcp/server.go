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

// Register validates the complete batch before adding any tool, so a rejected
// batch adds nothing. Handlers keep the identity snapshotted at registration
// even if a Tool later changes.
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
		tool := serverTool{executable: executable, definition: definition}
		// The low-level AddTool keeps the Tool's own schema; the generic SDK
		// helper would replace it with a reflected one.
		server.AddTool(tool.descriptor(), tool.handle)
	}
	return nil
}

type serverTool struct {
	executable toolcontract.Binding
	definition corechat.ToolDefinition
}

func (s serverTool) descriptor() *sdkmcp.Tool {
	return new(sdkmcp.Tool{
		Name:        s.definition.Name,
		Description: s.definition.Description,
		InputSchema: s.definition.InputSchema,
	})
}

// Invalid arguments and valid definite [toolcontract.Failure] outcomes become
// model-visible IsError results. Ordinary errors, invalid Failures, and
// unmappable outputs establish no definite Tool result, so they stay JSON-RPC
// internal errors that preserve that uncertainty without leaking diagnostics.
func (s serverTool) handle(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	toolName := s.definition.Name
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
	})
	if err != nil {
		recordSpanError(span, err)
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil
	}
	output, err := s.executable.Call(ctx, invocation)
	if err != nil {
		return s.callError(span, err)
	}
	result, err := mapServerToolOutput(output)
	if err != nil {
		return s.callError(span, err)
	}
	return result, nil
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
