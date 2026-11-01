package a2a_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/core/tool"
)

func TestWireRemoteTaskOutcomesAcrossBindings(t *testing.T) {
	for _, binding := range []string{"JSONRPC", "HTTP+JSON"} {
		for _, state := range []string{"message", "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED", "TASK_STATE_WORKING"} {
			t.Run(binding+"/"+state, func(t *testing.T) {
				var endpoint string
				var replyID atomic.Value
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					if request.Method == http.MethodGet {
						url, err := jsonv2.Marshal(endpoint)
						if err != nil {
							t.Error(err)
							return
						}
						_, _ = fmt.Fprintf(writer, `{"name":"Wire Agent","supportedInterfaces":[{"url":%s,"protocolBinding":%q,"protocolVersion":"1.0"}],"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"]}`, url, binding)
						return
					}
					var rpcID json.RawMessage
					var params json.RawMessage
					if binding == "JSONRPC" {
						var envelope struct {
							ID     json.RawMessage `json:"id"`
							Method string          `json:"method"`
							Params json.RawMessage `json:"params"`
						}
						if err := jsonv2.UnmarshalRead(request.Body, &envelope); err != nil {
							t.Error(err)
							return
						}
						if envelope.Method != "SendMessage" {
							t.Errorf("method = %s", envelope.Method)
						}
						rpcID, params = envelope.ID, envelope.Params
					} else if err := jsonv2.UnmarshalRead(request.Body, &params); err != nil {
						t.Error(err)
						return
					}
					var input struct {
						Message struct {
							Role      string `json:"role"`
							TaskID    string `json:"taskId"`
							ContextID string `json:"contextId"`
							ID        string `json:"messageId"`
							Parts     []struct {
								Text string `json:"text"`
							} `json:"parts"`
						} `json:"message"`
					}
					if err := jsonv2.Unmarshal(params, &input); err != nil {
						t.Error(err)
						return
					}
					wantText := "question 9007199254740993"
					resultState := state
					if input.Message.TaskID != "" {
						if state != "TASK_STATE_INPUT_REQUIRED" || input.Message.TaskID != "remote-task" || input.Message.ContextID != "remote-context" || input.Message.ID == "" {
							t.Errorf("wrong task continuation: %s", params)
						}
						if first := replyID.Load(); first != nil && first.(string) != input.Message.ID {
							t.Errorf("retry changed reply ID: %s", params)
						} else {
							replyID.Store(input.Message.ID)
						}
						wantText = "Paris"
						resultState = "TASK_STATE_COMPLETED"
					}
					if input.Message.Role != "ROLE_USER" || len(input.Message.Parts) != 1 || input.Message.Parts[0].Text != wantText {
						t.Errorf("request changed: %s", params)
					}
					result := fmt.Sprintf(`{"task":{"id":"remote-task","contextId":"remote-context","status":{"state":%q,"message":{"messageId":"status","role":"ROLE_AGENT","parts":[{"text":"status detail"}]}},"artifacts":[{"artifactId":"published","parts":[{"text":"published 9007199254740993"}]}]}}`, resultState)
					if state == "message" {
						result = `{"message":{"messageId":"reply","role":"ROLE_AGENT","parts":[{"text":"message 9007199254740993"}]}}`
					}
					if binding == "JSONRPC" {
						_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`, rpcID, result)
					} else {
						_, _ = fmt.Fprint(writer, result)
					}
				}))
				t.Cleanup(server.Close)
				endpoint = server.URL + "/rpc"
				tools, err := a2a.OpenToolSet(t.Context(), a2a.Endpoint{CardURL: server.URL, HTTPClient: server.Client()})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := tools.Close(); err != nil {
						t.Error(err)
					}
				})
				if state == "TASK_STATE_INPUT_REQUIRED" {
					b, err := tool.Bind(tools.Tools()[0])
					if err != nil {
						t.Fatal(err)
					}
					inv, err := b.Contract().Prepare(tool.Request{Name: b.Contract().Definition().Name, Arguments: `{"message":"question 9007199254740993"}`}, nil)
					if err != nil {
						t.Fatal(err)
					}
					pending, err := b.Call(t.Context(), inv)
					if err != nil || pending.InputRequest == nil || string(pending.InputRequest.Prompt()) != `"status detail"` {
						t.Fatalf("unbound task question: %v %v", pending, err)
					}
					c, err := pending.InputRequest.Continuation(json.RawMessage(`"Paris"`))
					if err != nil {
						t.Fatal(err)
					}
					original := tool.Request{Name: b.Contract().Definition().Name, Arguments: `{"message":"question 9007199254740993"}`}
					wrong := original
					wrong.Arguments = `{"message":"different"}`
					wrongInv, err := b.Contract().Prepare(wrong, &c)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = b.Call(t.Context(), wrongInv); !errors.Is(err, a2a.ErrContinuationBinding) {
						t.Fatalf("cross-call answer: %v", err)
					}
					for range 2 {
						next, prepareErr := b.Contract().Prepare(original, &c)
						if prepareErr != nil {
							t.Fatal(prepareErr)
						}
						complete, callErr := b.Call(t.Context(), next)
						if callErr != nil || complete.InputRequest != nil {
							t.Fatalf("continuation: %+v %v", complete, callErr)
						}
						if answer, _ := complete.Output.Text(); answer != "published 9007199254740993" {
							t.Fatal(answer)
						}
					}
					return
				}
				output, callErr := invokeTestTool(t.Context(), tools.Tools()[0], `{"message":"question 9007199254740993"}`)
				switch state {
				case "message", "TASK_STATE_COMPLETED":
					if callErr != nil {
						t.Fatal(callErr)
					}
					want := "published 9007199254740993"
					if state == "message" {
						want = "message 9007199254740993"
					}
					if text, _ := output.Text(); text != want {
						t.Fatalf("result = %q; want %q", text, want)
					}
				case "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED":
					failure, ok := errors.AsType[*tool.Failure](callErr)
					if !ok || failure.Kind() != tool.FailureKindFailed {
						t.Fatalf("error = %v; want definite task failure", callErr)
					}
					text, _ := failure.Output().Text()
					if !strings.Contains(text, "published 9007199254740993") || !strings.Contains(text, "status detail") {
						t.Fatalf("failure lost evidence: %s", text)
					}
				default:
					if _, ok := errors.AsType[*tool.Failure](callErr); ok {
						t.Fatalf("unfinished task became a definite failure: %v", callErr)
					}
					remote, ok := errors.AsType[*a2a.RemoteAgentError](callErr)
					if !ok || remote.State != sdka2a.TaskState(state) || len(output.Content) != 0 {
						t.Fatalf("unfinished task = %+v, %v", output, callErr)
					}
				}
			})
		}
	}
}
