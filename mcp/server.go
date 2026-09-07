package mcp

import (
	"context"
	"fmt"
	"mime"
	"slices"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// Register installs every [tool.Tool] in tools onto server using
// the low-level [(*sdkmcp.Server).AddTool] API.
//
// Registration is all-or-nothing: definitions are snapshotted, duplicate names
// within the batch are rejected, and every tool is built before any is added.
// A bad entry mid-list therefore never leaves the server half-registered, and
// handlers use the same identity the server advertised even when a Tool
// implementation is mutable.
// Structured details are handed to the SDK as raw JSON, preserving numeric
// precision and the distinction between explicit null and absent details.
//
// The generic sdkmcp.AddTool[In, Out] form is deliberately avoided:
// tools already supply a hand-authored JSON schema, and the
// generic API would otherwise reflect over a Go In type and overwrite
// it.
func Register(server *sdkmcp.Server, tools ...toolcontract.Tool) error {
	if server == nil {
		return ErrNilServer
	}

	registry, err := toolcontract.NewRegistry(tools...)
	if err != nil {
		return fmt.Errorf("mcp: register tools: %w", err)
	}

	prepared := make([]serverTool, 0, len(tools))
	for _, definition := range registry.Definitions() {
		executable, _ := registry.Resolve(definition.Name)
		prepared = append(prepared, serverTool{executable: executable, definition: definition})
	}
	for _, tool := range prepared {
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

// handle routes a tools/call RPC into a [tool.Tool]. Errors
// from the tool surface via [sdkmcp.CallToolResult.IsError] plus
// a [*sdkmcp.TextContent] body — never as a Go error from the handler
// — because the latter would be promoted to a JSON-RPC protocol error
// and hide the failure from the LLM's view.
//
// The MCP server session is stamped onto the context so tool authors
// can use the reverse-capability helpers ([ReportProgress] and [Elicit])
// without taking a direct dependency on the SDK session.
func (s serverTool) handle(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
	toolName := s.definition.Name
	ctx, span := mcpTracer.Start(ctx, "mcp.tool.serve "+toolName,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String(attrToolName, toolName)),
	)
	defer span.End()

	ctx = withServerCall(ctx, req)

	var rawArgs string
	if req != nil && req.Params != nil {
		rawArgs = string(req.Params.Arguments)
	}

	invocation, err := s.executable.Prepare(corechat.ToolCall{
		ID: "mcp/" + toolName, Name: toolName, Arguments: rawArgs,
	})
	if err != nil {
		return s.errorResult(span, err), nil
	}
	output, err := s.executable.Call(ctx, invocation)
	if err != nil {
		return s.errorResult(span, err), nil
	}
	result, err := mapServerToolOutput(output)
	if err != nil {
		return s.errorResult(span, err), nil
	}
	return result, nil
}

func (s serverTool) errorResult(span trace.Span, err error) *sdkmcp.CallToolResult {
	recordSpanError(span, err)
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
		IsError: true,
	}
}

func mapServerToolOutput(output corechat.ToolOutput) (*sdkmcp.CallToolResult, error) {
	if err := output.Validate(); err != nil {
		return nil, fmt.Errorf("mcp: invalid Tool output: %w", err)
	}
	result := &sdkmcp.CallToolResult{Content: make([]sdkmcp.Content, 0, len(output.Content))}
	for index := range output.Content {
		part := output.Content[index]
		switch part.Kind {
		case corechat.PartText:
			result.Content = append(result.Content, &sdkmcp.TextContent{Text: part.Text})
		case corechat.PartMedia:
			content, err := mapServerMedia(part.Media)
			if err != nil {
				return nil, fmt.Errorf("mcp: tool output content[%d]: %w", index, err)
			}
			result.Content = append(result.Content, content)
		default:
			return nil, fmt.Errorf("mcp: tool output content[%d]: unsupported part %q", index, part.Kind)
		}
	}
	if len(output.Details) != 0 {
		result.StructuredContent = slices.Clone(output.Details)
	}
	return result, nil
}

func mapServerMedia(value *media.Media) (sdkmcp.Content, error) {
	mediaType, _, err := mime.ParseMediaType(value.MIME)
	if err != nil {
		return nil, err
	}
	if value.Source.Kind == media.SourceBytes {
		data, bytesErr := value.Bytes()
		if bytesErr != nil {
			return nil, bytesErr
		}
		switch {
		case strings.HasPrefix(mediaType, "image/"):
			return &sdkmcp.ImageContent{MIMEType: value.MIME, Data: data}, nil
		case strings.HasPrefix(mediaType, "audio/"):
			return &sdkmcp.AudioContent{MIMEType: value.MIME, Data: data}, nil
		default:
			return &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{
				URI: "scope://tool-output/" + value.Name, MIMEType: value.MIME, Blob: data,
			}}, nil
		}
	}
	var uri string
	switch value.Source.Kind {
	case media.SourceURI:
		uri, err = value.URI()
	case media.SourceReference:
		uri, err = value.Reference()
	default:
		return nil, media.ErrInvalidSource
	}
	if err != nil {
		return nil, err
	}
	return &sdkmcp.ResourceLink{URI: uri, Name: value.Name, MIMEType: value.MIME}, nil
}
