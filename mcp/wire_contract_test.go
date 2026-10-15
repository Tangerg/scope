package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

const wireMCPVersion = "2026-07-28"

// The peer uses independent JSON wire values; neither its descriptor nor its
// results pass through the SDK encoder before reaching the client decoder.
type wireMCPPeer struct {
	t          *testing.T
	descriptor string
	call       func(json.RawMessage) string
}

func (w wireMCPPeer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := jsonv2.UnmarshalRead(request.Body, &message); err != nil {
		w.t.Error(err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(message.ID) == 0 {
		writer.WriteHeader(http.StatusAccepted)
		return
	}
	var result string
	switch message.Method {
	case "server/discover":
		result = fmt.Sprintf(`{"supportedVersions":[%q],"capabilities":{"tools":{}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"wire-peer","version":"1"}}}`, wireMCPVersion)
	case "tools/list":
		result = `{"tools":[` + w.descriptor + `]}`
	case "tools/call":
		result = w.call(message.Params)
	default:
		w.t.Errorf("unexpected method %q", message.Method)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, message.ID, result)
}

func connectWireMCP(t *testing.T, peer wireMCPPeer) *sdkmcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(peer)
	t.Cleanup(server.Close)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "scope-client", Version: "1"}, &sdkmcp.ClientOptions{MultiRoundTrip: &sdkmcp.MultiRoundTripOptions{Disabled: true}})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: server.Client(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	return session
}

func orderedStructuredContent(t *testing.T, raw []byte) string {
	t.Helper()
	if len(raw) == 0 {
		return ""
	}
	value := jsontext.Value(bytes.Clone(raw))
	// Object order is immaterial; raw formatting must retain exact numbers.
	if err := value.Format(jsontext.ReorderRawObjects(true)); err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func TestStructuredContentComparisonPreservesValues(t *testing.T) {
	for _, test := range []struct {
		name, want, got string
		equal           bool
	}{
		{
			name:  "object members may reorder recursively",
			want:  `{"n":18446744073709551615,"fraction":0.12345678901234567890123456789,"nested":[{"b":2,"a":1}]}`,
			got:   `{"nested":[{"a":1,"b":2}],"fraction":0.12345678901234567890123456789,"n":18446744073709551615}`,
			equal: true,
		},
		{name: "integer precision", want: `{"n":9007199254740993}`, got: `{"n":9007199254740992}`},
		{name: "fraction precision", want: `{"n":0.12345678901234567890123456789}`, got: `{"n":0.12345678901234567890123456788}`},
		{name: "null is present", want: "", got: "null"},
		{name: "empty object is present", want: "", got: `{}`},
		{name: "empty array is present", want: "", got: `[]`},
		{name: "object member is present", want: `{"n":null}`, got: `{}`},
		{name: "array order", want: `[null,9007199254740993]`, got: `[9007199254740993,null]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			equal := orderedStructuredContent(t, []byte(test.want)) == orderedStructuredContent(t, []byte(test.got))
			if equal != test.equal {
				t.Fatalf("structured content equality = %t, want %t: %s versus %s", equal, test.equal, test.want, test.got)
			}
		})
	}
}

func TestWireStructuredContentAndFailureAreLossless(t *testing.T) {
	for _, payload := range []string{"", "null", "9007199254740993", `{"n":18446744073709551615,"fraction":0.12345678901234567890123456789}`, `{"outer":{"n":9007199254740993,"fraction":0.12345678901234567890123456789},"array":[null,{"z":1,"a":2}]}`, `[null,9007199254740993]`, "false", `"text"`} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error=%t", payload, failed), func(t *testing.T) {
				session := connectWireMCP(t, wireMCPPeer{t: t, descriptor: `{"name":"probe","inputSchema":{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}}`, call: func(encoded json.RawMessage) string {
					var params struct {
						Arguments struct {
							N json.RawMessage `json:"n"`
						} `json:"arguments"`
						Meta struct {
							N json.RawMessage `json:"n"`
						} `json:"_meta"`
					}
					if err := jsonv2.Unmarshal(encoded, &params); err != nil {
						t.Error(err)
					}
					if string(params.Arguments.N) != "9007199254740993" || string(params.Meta.N) != "9007199254740993" {
						t.Errorf("request numbers changed: %s", encoded)
					}
					result := fmt.Sprintf(`{"content":[{"type":"text","text":"visible","_meta":{"scope/content":"{\"metadata\":{\"record\":9007199254740993}}"}}],"isError":%t`, failed)
					if payload != "" {
						result += `,"structuredContent":` + payload
					}
					return result + `}`
				}})
				tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Name: "wire", Session: session}}, scopemcp.ToolDiscoveryConfig{RequestMeta: scopemcp.RequestMetaFromContext})
				if err != nil {
					t.Fatal(err)
				}
				if len(tools) != 1 {
					t.Fatalf("tools = %d", len(tools))
				}
				ctx := scopemcp.WithRequestMeta(t.Context(), sdkmcp.Meta{"n": json.RawMessage(`9007199254740993`)})
				output, callErr := invokeTestTool(ctx, tools[0], `{"n":9007199254740993}`)
				if failed {
					failure, ok := errors.AsType[*tool.Failure](callErr)
					if !ok || failure.Kind() != tool.FailureKindFailed {
						t.Fatalf("error = %v; want definite failure", callErr)
					}
					output = failure.Output()
				} else if callErr != nil {
					t.Fatal(callErr)
				}
				if orderedStructuredContent(t, output.Details) != orderedStructuredContent(t, []byte(payload)) || len(output.Content) != 1 || output.Content[0].Text != "visible" || string(output.Content[0].Metadata["record"]) != "9007199254740993" {
					t.Fatalf("wire result changed: %+v", output)
				}
			})
		}
	}
}

func TestWireInputRequiredIsNotACompletion(t *testing.T) {
	const state = "opaque-state/9007199254740993"
	session := connectWireMCP(t, wireMCPPeer{t: t, descriptor: `{"name":"probe","inputSchema":{"type":"object"}}`, call: func(encoded json.RawMessage) string {
		var params struct {
			State string `json:"requestState"`
		}
		if err := jsonv2.Unmarshal(encoded, &params); err != nil {
			t.Error(err)
		}
		if params.State == "" {
			return `{"resultType":"input_required","inputRequests":{},"requestState":"` + state + `"}`
		}
		if params.State != state {
			t.Errorf("state = %q", params.State)
		}
		return `{"content":[],"structuredContent":{"n":9007199254740993}}`
	}})
	tools, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	output, err := invokeTestTool(t.Context(), tools[0], `{}`)
	if !errors.Is(err, scopemcp.ErrIncompleteResult) || len(output.Content) != 0 || len(output.Details) != 0 {
		t.Fatalf("incomplete result = %+v, %v", output, err)
	}
	pending, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if err != nil || !pending.NeedsInput() || pending.RequestState != state {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	complete, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "probe", Arguments: json.RawMessage(`{}`), RequestState: pending.RequestState})
	if err != nil || complete.NeedsInput() {
		t.Fatalf("complete = %+v, %v", complete, err)
	}
	encoded, err := jsonv2.Marshal(complete.StructuredContent)
	if err != nil || string(encoded) != `{"n":9007199254740993}` {
		t.Fatalf("completed value = %s, %v", encoded, err)
	}
}

func TestWireRegistrationPreservesArgumentsSchemaAndResults(t *testing.T) {
	for _, payload := range []string{"", "null", `{"n":9007199254740993,"fraction":0.12345678901234567890123456789}`} {
		t.Run(payload, func(t *testing.T) {
			const schema = `{"type":"object","properties":{"n":{"type":"integer","const":9007199254740993}},"required":["n"]}`
			received := make(chan string, 1)
			executable := testTool{definition: chat.ToolDefinition{Name: "probe", InputSchema: json.RawMessage(schema)}, call: func(ctx context.Context, invocation tool.Invocation) (chat.ToolOutput, error) {
				received <- string(invocation.Arguments())
				return chat.ToolOutput{Details: json.RawMessage(payload)}, nil
			}}
			server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "scope-server", Version: "1"}, nil)
			if err := scopemcp.Register(server, executable); err != nil {
				t.Fatal(err)
			}
			transport := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
			t.Cleanup(transport.Close)
			post := func(body string) json.RawMessage {
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, transport.URL, bytes.NewBufferString(body))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Accept", "application/json, text/event-stream")
				request.Header.Set("Mcp-Protocol-Version", wireMCPVersion)
				var routing struct {
					Method string `json:"method"`
					Params struct {
						Name string `json:"name"`
					} `json:"params"`
				}
				if decodeErr := jsonv2.Unmarshal([]byte(body), &routing); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				request.Header.Set("Mcp-Method", routing.Method)
				if routing.Params.Name != "" {
					request.Header.Set("Mcp-Name", routing.Params.Name)
				}
				client := &http.Client{Timeout: 5 * time.Second}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				encoded, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				var envelope struct {
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				if err := jsonv2.Unmarshal(encoded, &envelope); err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusOK || len(envelope.Error) != 0 {
					t.Fatalf("wire response: %d, %s", response.StatusCode, encoded)
				}
				return envelope.Result
			}
			listed := post(fmt.Sprintf(`{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":%q,"io.modelcontextprotocol/clientCapabilities":{}}}}`, wireMCPVersion))
			if !bytes.Contains(listed, []byte(`9007199254740993`)) {
				t.Fatalf("schema lost precision: %s", listed)
			}
			encoded := post(fmt.Sprintf(`{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":%q,"io.modelcontextprotocol/clientCapabilities":{}},"name":"probe","arguments":{"n":9007199254740993}}}`, wireMCPVersion))
			select {
			case arguments := <-received:
				if arguments != `{"n":9007199254740993}` {
					t.Fatalf("arguments = %s", arguments)
				}
			default:
				t.Fatal("validated arguments did not reach the tool")
			}
			var result struct {
				Structured json.RawMessage `json:"structuredContent"`
				IsError    bool            `json:"isError"`
			}
			if err := jsonv2.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			if result.IsError || orderedStructuredContent(t, result.Structured) != orderedStructuredContent(t, []byte(payload)) {
				t.Fatalf("structured content changed: %s", encoded)
			}
		})
	}
}
