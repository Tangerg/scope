package mcp_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func connectContentServer(t *testing.T, server *sdkmcp.Server) *sdkmcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "content-test"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, session.Close())
		require.NoError(t, serverSession.Close())
	})
	return session
}

func TestRemoteOutputSchemaAdmission(t *testing.T) {
	const objectSchema = `{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`
	for _, test := range []struct {
		name, schema, details string
		isError, invalid      bool
	}{
		{name: "valid", schema: objectSchema, details: `{"count":7}`},
		{name: "wrong type", schema: objectSchema, details: `{"count":"7"}`, invalid: true},
		{name: "missing property", schema: objectSchema, details: `{}`, invalid: true},
		{name: "missing content", schema: objectSchema, invalid: true},
		{name: "nullable null", schema: `{"type":["integer","null"]}`, details: `null`},
		{name: "null is not absent", schema: `{"type":"null"}`, invalid: true},
		{name: "exact large integer", schema: `{"type":"integer"}`, details: `9007199254740993`},
		{name: "noninteger result", schema: `{"type":"integer"}`, details: `0.5`, invalid: true},
		{name: "schema absent", details: `"anything"`},
		{name: "failure has no success schema obligation", schema: objectSchema, details: `{"error":"failed"}`, isError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "schema-test"}, nil)
			descriptor := &sdkmcp.Tool{Name: "inspect", InputSchema: json.RawMessage(`{"type":"object"}`)}
			if test.schema != "" {
				descriptor.OutputSchema = json.RawMessage(test.schema)
			}
			server.AddTool(descriptor, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				result := &sdkmcp.CallToolResult{IsError: test.isError, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "visible"}}}
				if test.details != "" {
					result.StructuredContent = json.RawMessage(test.details)
				}
				return result, nil
			})
			session := connectContentServer(t, server)
			tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
			require.NoError(t, err)
			output, err := invokeTestTool(t.Context(), tools[0], `{}`)
			if test.invalid {
				require.Error(t, err)
				require.Empty(t, output.Content)
				_, failure := errors.AsType[*tool.Failure](err)
				require.False(t, failure, "invalid success is a protocol error, not a definite Tool failure")
				return
			}
			if test.isError {
				failure, found := errors.AsType[*tool.Failure](err)
				require.True(t, found)
				output = failure.Output()
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.details, string(output.Details))
		})
	}
}

func TestMCPContentRoundTripThroughDiscoveryAndRegistration(t *testing.T) {
	annotations := &sdkmcp.Annotations{Audience: []sdkmcp.Role{"assistant"}, Priority: 0.75, LastModified: "2026-09-27T00:00:00Z"}
	meta := sdkmcp.Meta{"example/value": json.RawMessage(`{"id":42}`)}
	contents := []sdkmcp.Content{
		&sdkmcp.TextContent{Text: "visible text", Meta: meta, Annotations: annotations},
		&sdkmcp.ImageContent{Data: []byte{1, 2, 3}, MIMEType: "image/png", Meta: meta, Annotations: annotations},
		&sdkmcp.AudioContent{Data: []byte{4, 5}, MIMEType: "audio/mpeg", Meta: meta, Annotations: annotations},
		&sdkmcp.ResourceLink{URI: "https://example.com/report.pdf", Name: "report", MIMEType: "application/pdf", Title: "A report", Description: "Report description", Size: new(int64(42)), Icons: []sdkmcp.Icon{{Source: "https://example.com/icon.png"}}, Meta: meta, Annotations: annotations},
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{URI: "file:///repo/example.go", MIMEType: "text/plain", Text: "source body", Meta: meta}, Meta: meta, Annotations: annotations},
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{URI: "file:///repo/image.png", MIMEType: "image/png", Blob: []byte{6, 7}, Meta: meta}, Meta: meta, Annotations: annotations},
		&sdkmcp.ResourceLink{URI: "https://example.com/photo.JPG", Name: "photo"},
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{URI: "file:///repo/clip.wav", Blob: []byte{8}}},
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{URI: "https://example.com/notes.md"}},
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "source"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: contents}, nil
	})
	session := connectContentServer(t, server)
	tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	output, err := invokeTestTool(t.Context(), tools[0], `{}`)
	require.NoError(t, err)
	require.Equal(t, "source body", output.Content[4].Text)
	require.Contains(t, string(output.Content[4].Metadata[scopemcp.ContentMetadataKey]), "file:///repo/example.go")
	require.NotContains(t, string(output.Content[3].Metadata[scopemcp.ContentMetadataKey]), "mimeTypeInferred")
	for index, want := range []struct{ mime, envelope string }{
		{mime: "image/jpeg", envelope: `{"type":"resource_link","mimeTypeInferred":true}`},
		{mime: "audio/wav", envelope: `{"type":"resource","resource":{"uri":"file:///repo/clip.wav"},"mimeTypeInferred":true}`},
		{mime: "application/octet-stream", envelope: `{"type":"resource","resource":{},"mimeTypeInferred":true}`},
	} {
		part := output.Content[6+index]
		require.Equal(t, want.mime, part.Media.MIME)
		require.Equal(t, want.envelope, string(part.Metadata[scopemcp.ContentMetadataKey]))
	}
	for index, content := range contents {
		messages, promptErr := scopemcp.PromptMessagesToChat([]*sdkmcp.PromptMessage{{Role: "user", Content: content}})
		require.NoError(t, promptErr)
		require.Equal(t, output.Content[index].Metadata, messages[0].Parts[0].Metadata)
	}
	proxy := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "proxy"}, nil)
	require.NoError(t, scopemcp.Register(proxy, tools...))
	proxySession := connectContentServer(t, proxy)
	result, err := proxySession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "read", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	want, err := jsonv2.Marshal(contents)
	require.NoError(t, err)
	got, err := jsonv2.Marshal(result.Content)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
	require.Contains(t, string(got), `"id":42`)
}

func TestCoreContentMetadataAndCitationsRoundTrip(t *testing.T) {
	var values metadata.Map
	require.NoError(t, values.Set("app/value", json.RawMessage(`9007199254740993`)))
	output := chat.ToolOutput{Content: []chat.ToolContent{{Kind: chat.PartText, Text: "evidence", Metadata: values,
		Citations: []chat.Citation{{Source: chat.CitationSource{Kind: chat.CitationSourceURI, Value: "https://example.com/source"}, Title: "Source"}},
	}}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "core-source"}, nil)
	local := contentOutputTool{output: output}
	require.NoError(t, scopemcp.Register(server, local))
	session := connectContentServer(t, server)
	tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	got, err := invokeTestTool(t.Context(), tools[0], `{}`)
	require.NoError(t, err)
	require.Equal(t, output, got)
}

func TestCoreMediaNamesRoundTrip(t *testing.T) {
	image, err := media.NewBytes("image/png", []byte{1})
	require.NoError(t, err)
	image.Name = "chart.png"
	audio, err := media.NewBytes("audio/mpeg", []byte{2})
	require.NoError(t, err)
	audio.Name = "voice.mp3"
	document, err := media.NewBytes("application/pdf", []byte{3})
	require.NoError(t, err)
	document.Name = "report.pdf"
	linked, err := media.NewURI("image/png", "https://example.com/a.png")
	require.NoError(t, err)
	linked.Name = "site"
	parts := []*media.Media{image, audio, document, linked}
	output := chat.ToolOutput{}
	for _, value := range parts {
		output.Content = append(output.Content, chat.ToolContent{Kind: chat.PartMedia, Media: value})
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "named-media"}, nil)
	require.NoError(t, scopemcp.Register(server, contentOutputTool{output: output}))
	session := connectContentServer(t, server)

	result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "content", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	wire, err := jsonv2.Marshal(result.Content)
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"type":"image","mimeType":"image/png","data":"AQ==","_meta":{"scope/content":"{\"name\":\"chart.png\"}"}},
		{"type":"audio","mimeType":"audio/mpeg","data":"Ag==","_meta":{"scope/content":"{\"name\":\"voice.mp3\"}"}},
		{"type":"resource","resource":{"uri":"scope://tool-output/report.pdf","mimeType":"application/pdf","blob":"Aw=="},"_meta":{"scope/content":"{\"name\":\"report.pdf\"}"}},
		{"type":"resource_link","uri":"https://example.com/a.png","name":"site","mimeType":"image/png"}
	]`, string(wire))

	tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
	require.NoError(t, err)
	got, err := invokeTestTool(t.Context(), tools[0], `{}`)
	require.NoError(t, err)
	require.Len(t, got.Content, len(parts))
	for index, value := range parts {
		require.Equal(t, value, got.Content[index].Media)
	}
}

type contentOutputTool struct{ output chat.ToolOutput }

func (c contentOutputTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{Name: "content", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (c contentOutputTool) Call(context.Context, tool.Invocation) (chat.ToolOutput, error) {
	return c.output.Clone(), nil
}
