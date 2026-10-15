package a2a_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					if request.Method == http.MethodGet {
						url, err := jsonv2.Marshal(endpoint)
						if err != nil {
							t.Error(err)
							return
						}
						_, _ = fmt.Fprintf(writer, `{"name":"Wire Agent","supportedInterfaces":[{"url":%s,"protocolBinding":%q,"protocolVersion":"1.0"}],"defaultInputModes":["text"],"defaultOutputModes":["text"]}`, url, binding)
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
							Role  string `json:"role"`
							Parts []struct {
								Text string `json:"text"`
							} `json:"parts"`
						} `json:"message"`
					}
					if err := jsonv2.Unmarshal(params, &input); err != nil {
						t.Error(err)
						return
					}
					if input.Message.Role != "ROLE_USER" || len(input.Message.Parts) != 1 || input.Message.Parts[0].Text != "question 9007199254740993" {
						t.Errorf("request changed: %s", params)
					}
					result := fmt.Sprintf(`{"task":{"id":"remote-task","contextId":"remote-context","status":{"state":%q,"message":{"messageId":"status","role":"ROLE_AGENT","parts":[{"text":"status detail"}]}},"artifacts":[{"artifactId":"published","parts":[{"text":"published 9007199254740993"}]}]}}`, state)
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
