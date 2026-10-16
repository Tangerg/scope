package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestWireSchemaPreservesNumericConstraints(t *testing.T) {
	for _, test := range []struct{ name, constraint, valid, invalid string }{
		{"large const", `{"const":9007199254740993}`, `9007199254740993`, `9007199254740992`},
		{"nested enum", `{"type":"object","properties":{"values":{"type":"array","items":{"enum":[9007199254740993,18446744073709551615]}}},"required":["values"]}`, `{"values":[9007199254740993,18446744073709551615]}`, `{"values":[9007199254740992]}`},
		{"integer minimum", `{"minimum":9007199254740993}`, `9007199254740993`, `9007199254740992`},
		{"integer maximum", `{"maximum":18446744073709551615}`, `18446744073709551615`, `18446744073709551616`},
		{"decimal const", `{"const":0.12345678901234567890123456789}`, `0.12345678901234567890123456789`, `0.12345678901234567890123456788`},
		{"decimal bounds", `{"minimum":0.12345678901234567890123456789,"exclusiveMaximum":0.12345678901234567890123456791}`, `0.12345678901234567890123456790`, `0.12345678901234567890123456788`},
	} {
		for _, reordered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reordered=%t", test.name, reordered), func(t *testing.T) {
				schema := `{"type":"object","properties":{"n":` + test.constraint + `},"required":["n"]}`
				if reordered {
					schema = `{"required":["n"],"properties":{"n":` + test.constraint + `},"type":"object"}`
				}
				valid := `{"n":` + test.valid + `}`
				invalid := `{"n":` + test.invalid + `}`
				var calls atomic.Int32
				var invalidOutput atomic.Bool
				session := connectWireMCP(t, wireMCPPeer{t: t, descriptor: `{"name":"exact","inputSchema":` + schema + `,"outputSchema":` + schema + `}`, call: func(json.RawMessage) string {
					calls.Add(1)
					value := valid
					if invalidOutput.Load() {
						value = invalid
					}
					return `{"content":[],"structuredContent":` + value + `}`
				}})
				tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
				if err != nil {
					t.Fatal(err)
				}
				output, err := invokeTestTool(t.Context(), tools[0], valid)
				if err != nil || orderedStructuredContent(t, output.Details) != orderedStructuredContent(t, []byte(valid)) {
					t.Fatalf("valid result = %+v, %v", output, err)
				}
				if _, invalidErr := invokeTestTool(t.Context(), tools[0], invalid); invalidErr == nil {
					t.Fatal("invalid input admitted")
				}
				if calls.Load() != 1 {
					t.Fatalf("invalid input reached remote: calls=%d", calls.Load())
				}
				invalidOutput.Store(true)
				output, err = invokeTestTool(t.Context(), tools[0], valid)
				if err == nil || len(output.Content) != 0 || len(output.Details) != 0 {
					t.Fatalf("invalid output = %+v, %v", output, err)
				}
				if _, definite := errors.AsType[*tool.Failure](err); definite {
					t.Fatalf("output validation became a definite failure: %v", err)
				}
			})
		}
	}
}

func TestWireEmbeddedResourceSourceAndPresence(t *testing.T) {
	for _, test := range []struct {
		name, content string
		valid, empty  bool
	}{
		{name: "text", content: `{"type":"resource","_meta":{"content":42},"resource":{"uri":"file:///evidence","text":"body","_meta":{"resource":7}}}`, valid: true},
		{name: "blob", content: `{"type":"resource","resource":{"uri":"file:///evidence","blob":"AQ=="}}`, valid: true},
		{name: "empty text", content: `{"type":"resource","resource":{"uri":"file:///evidence","text":""}}`, empty: true},
		{name: "empty blob", content: `{"type":"resource","resource":{"uri":"file:///evidence","blob":""}}`, empty: true},
		{name: "URI only", content: `{"type":"resource","resource":{"uri":"file:///evidence"}}`},
		{name: "text null", content: `{"type":"resource","resource":{"uri":"file:///evidence","text":null}}`},
		{name: "blob null", content: `{"type":"resource","resource":{"uri":"file:///evidence","blob":null}}`},
		{name: "text number", content: `{"type":"resource","resource":{"uri":"file:///evidence","text":1}}`},
		{name: "blob array", content: `{"type":"resource","resource":{"uri":"file:///evidence","blob":[1]}}`},
		{name: "blob number", content: `{"type":"resource","resource":{"uri":"file:///evidence","blob":1}}`},
		{name: "empty text and blob", content: `{"type":"resource","resource":{"uri":"file:///evidence","text":"","blob":"AQ=="}}`},
		{name: "text and empty blob", content: `{"type":"resource","resource":{"uri":"file:///evidence","text":"body","blob":""}}`},
		{name: "missing URI", content: `{"type":"resource","resource":{"text":"body"}}`},
		{name: "real link", content: `{"type":"resource_link","uri":"https://example.com/evidence","name":"evidence"}`, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := connectWireMCP(t, wireMCPPeer{t: t, descriptor: `{"name":"read","inputSchema":{"type":"object"}}`, call: func(json.RawMessage) string { return `{"content":[` + test.content + `]}` }})
			tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
			if err != nil {
				t.Fatal(err)
			}
			output, callErr := invokeTestTool(t.Context(), tools[0], `{}`)
			if test.valid {
				if callErr != nil || len(output.Content) != 1 {
					t.Fatalf("result = %+v, %v", output, callErr)
				}
				switch test.name {
				case "text":
					if output.Content[0].Text != "body" || string(output.Content[0].Metadata[scopemcp.ContentMetadataKey]) != `{"type":"resource","_meta":{"content":42},"resource":{"uri":"file:///evidence","_meta":{"resource":7}}}` {
						t.Fatalf("text or provenance changed: %+v", output.Content[0])
					}
				case "blob":
					data, err := output.Content[0].Media.Bytes()
					if err != nil || !bytes.Equal(data, []byte{1}) || output.Content[0].Media.Source.Kind != media.SourceBytes {
						t.Fatalf("inline source changed: %+v, %v", output.Content[0], err)
					}
				case "real link":
					uri, err := output.Content[0].Media.URI()
					if err != nil || uri != "https://example.com/evidence" || output.Content[0].Media.Name != "evidence" {
						t.Fatalf("resource link changed: %+v, %v", output.Content[0], err)
					}
				}
			} else {
				if callErr == nil || len(output.Content) != 0 {
					t.Fatalf("unsupported content became success: %+v, %v", output, callErr)
				}
				if _, definite := errors.AsType[*tool.Failure](callErr); definite {
					t.Fatalf("translation error became definite failure: %v", callErr)
				}
				if test.empty && !errors.Is(callErr, scopemcp.ErrEmptyEmbeddedResource) {
					t.Fatalf("empty resource = %v", callErr)
				}
			}
			var prompt sdkmcp.PromptMessage
			decodeErr := jsonv2.Unmarshal([]byte(`{"role":"user","content":`+test.content+`}`), &prompt)
			if decodeErr != nil {
				if test.valid || test.empty {
					t.Fatal(decodeErr)
				}
				return
			}
			messages, promptErr := scopemcp.PromptMessagesToChat([]*sdkmcp.PromptMessage{&prompt})
			if test.valid {
				if promptErr != nil || len(messages) != 1 || len(messages[0].Parts) != 1 {
					t.Fatalf("prompt = %+v, %v", messages, promptErr)
				}
			} else if promptErr == nil || len(messages) != 0 {
				t.Fatalf("unsupported prompt = %+v, %v", messages, promptErr)
			}
			if test.empty && !errors.Is(promptErr, scopemcp.ErrEmptyEmbeddedResource) {
				t.Fatalf("empty prompt = %v", promptErr)
			}
		})
	}
}

func TestServingEmbeddedResourceCannotReclassifyURI(t *testing.T) {
	for _, uri := range []string{"", "file:///provenance"} {
		t.Run(uri, func(t *testing.T) {
			linked, err := media.NewURI("application/pdf", "https://example.com/evidence")
			if err != nil {
				t.Fatal(err)
			}
			part := chat.ToolOutput{Content: []chat.ToolContent{{Kind: chat.PartMedia, Media: linked}}}
			if err := part.Content[0].Metadata.Set(scopemcp.ContentMetadataKey, map[string]any{"type": "resource", "resource": map[string]any{"uri": uri}}); err != nil {
				t.Fatal(err)
			}
			server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "invalid-source"}, nil)
			executable := testTool{definition: chat.ToolDefinition{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, call: func(context.Context, tool.Invocation) (chat.ToolOutput, error) { return part, nil }}
			if err := scopemcp.Register(server, executable); err != nil {
				t.Fatal(err)
			}
			session := connectContentServer(t, server)
			if result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "read"}); err == nil {
				t.Fatalf("URI emitted as inline resource: %+v", result)
			}
		})
	}
}
