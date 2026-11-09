package mcp_test

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestResourceLinkWirePreservesRequiredNameAndOptionalFields(t *testing.T) {
	for _, name := range []string{"", "report"} {
		t.Run(fmt.Sprintf("name=%q", name), func(t *testing.T) {
			linked, err := media.NewURI("application/pdf", "https://example.com/report.pdf")
			require.NoError(t, err)
			linked.Name = name
			var values metadata.Map
			require.NoError(t, values.Set(scopemcp.ContentMetadataKey, json.RawMessage(`{
				"type":"resource_link","title":"A report","description":"Evidence","size":0,
				"icons":[{"src":"https://example.com/icon.png"}],
				"annotations":{"audience":["assistant"],"priority":0.75,"lastModified":"2026-09-27T00:00:00Z"},
				"_meta":{"record":9007199254740993}
			}`)))
			output := tool.Output{Content: []corecontent.Block{{Kind: corecontent.Media, Media: linked, Metadata: values}}}
			server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "resource-link"}, nil)
			require.NoError(t, registerTestTools(server, contentOutputTool{output: output}))
			transport := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
			t.Cleanup(transport.Close)
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":%q,"io.modelcontextprotocol/clientCapabilities":{}},"name":"content","arguments":{}}}`, wireMCPVersion)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, transport.URL, bytes.NewBufferString(body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("Mcp-Protocol-Version", wireMCPVersion)
			request.Header.Set("Mcp-Method", "tools/call")
			request.Header.Set("Mcp-Name", "content")
			response, err := transport.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			var envelope struct {
				Result struct {
					Content []json.RawMessage `json:"content"`
				} `json:"result"`
				Error json.RawMessage `json:"error"`
			}
			require.NoError(t, jsonv2.UnmarshalRead(response.Body, &envelope))
			require.Empty(t, envelope.Error)
			require.Len(t, envelope.Result.Content, 1)
			want := fmt.Sprintf(`{
				"type":"resource_link","uri":"https://example.com/report.pdf","name":%q,
				"mimeType":"application/pdf","title":"A report","description":"Evidence","size":0,
				"icons":[{"src":"https://example.com/icon.png"}],
				"annotations":{"audience":["assistant"],"priority":0.75,"lastModified":"2026-09-27T00:00:00Z"},
				"_meta":{"record":9007199254740993}
			}`, name)
			require.Equal(t, orderedStructuredContent(t, []byte(want)), orderedStructuredContent(t, envelope.Result.Content[0]))
		})
	}
}

func TestResourceLinkCanBeMappedBeforeTransportEncoding(t *testing.T) {
	linked, err := media.NewURI("application/pdf", "https://example.com/report.pdf")
	require.NoError(t, err)
	encoded, err := scopemcp.EncodeToolResult(tool.Output{Content: []corecontent.Block{{Kind: corecontent.Media, Media: linked}}})
	require.NoError(t, err)
	converted, err := scopemcp.PromptMessagesToChat([]*sdkmcp.PromptMessage{{Role: "user", Content: encoded.Content[0]}})
	require.NoError(t, err)
	require.Len(t, converted, 1)
	require.Equal(t, linked, converted[0].Parts[0].Media)
}
