package bedrock_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestChatAdmitsToolHistoryOnlyWithEnabledDefinitions(t *testing.T) {
	for _, test := range []struct {
		name        string
		history     bool
		definitions bool
		choice      *corechat.ToolChoice
		unsupported bool
	}{
		{name: "no tools"},
		{name: "disabled tools without history", definitions: true, choice: &corechat.ToolChoice{Mode: corechat.ToolChoiceNone}},
		{name: "history with default choice", history: true, definitions: true},
		{name: "history with auto choice", history: true, definitions: true, choice: &corechat.ToolChoice{Mode: corechat.ToolChoiceAuto}},
		{name: "history with required choice", history: true, definitions: true, choice: &corechat.ToolChoice{Mode: corechat.ToolChoiceRequired}},
		{name: "history with named choice", history: true, definitions: true, choice: &corechat.ToolChoice{Mode: corechat.ToolChoiceNamed, Name: "weather"}},
		{name: "history with disabled tools", history: true, definitions: true, choice: &corechat.ToolChoice{Mode: corechat.ToolChoiceNone}, unsupported: true},
		{name: "history without definitions", history: true, unsupported: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				var body struct {
					Messages []struct {
						Content []struct {
							ToolUse    json.RawMessage `json:"toolUse"`
							ToolResult json.RawMessage `json:"toolResult"`
						} `json:"content"`
					} `json:"messages"`
					ToolConfig json.RawMessage `json:"toolConfig"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
					http.Error(writer, "invalid request", http.StatusBadRequest)
					return
				}
				if test.history {
					if len(body.Messages) != 3 || len(body.Messages[1].Content) != 1 || len(body.Messages[2].Content) != 1 || len(body.Messages[1].Content[0].ToolUse) == 0 || len(body.Messages[2].Content[0].ToolResult) == 0 || len(body.ToolConfig) == 0 {
						t.Errorf("tool history or configuration was lost: %#v", body)
					}
				} else if len(body.ToolConfig) != 0 {
					t.Errorf("disabled tool configuration = %s", body.ToolConfig)
				}
				writeBedrockBehaviorEvent(writer)
				writeBedrockEvent(writer, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
				writeBedrockEvent(writer, "messageStop", []byte(`{"stopReason":"end_turn"}`))
			}))
			t.Cleanup(server.Close)
			model := newBedrockBehaviorChat(t, server.URL)
			request := newBedrockBehaviorRequest(t)
			request.ToolChoice = test.choice
			if test.definitions {
				request.Tools = []toolcontract.Definition{{Name: "weather", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			}
			if test.history {
				request.Messages = append(request.Messages,
					corechat.NewAssistantMessage(corechat.NewToolCallPart(corechat.ToolCall{ID: "call-1", Name: "weather", Arguments: `{}`})),
					corechat.NewToolMessage(corechat.ToolResult{ID: "call-1", Name: "weather", Output: toolcontract.NewTextOutput("sunny")}),
				)
			}
			if err := request.Validate(); err != nil {
				t.Fatalf("invalid Core test request: %v", err)
			}
			for _, stream := range []bool{false, true} {
				var response *corechat.Response
				var err error
				if stream {
					response, err = corechat.Collect(model.Stream(t.Context(), request))
				} else {
					response, err = model.Call(t.Context(), request)
				}
				if test.unsupported {
					if response != nil || !errors.Is(err, errors.ErrUnsupported) || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
						t.Fatalf("stream=%t: response=%v error=%v calls=%d", stream, response, err, calls.Load())
					}
				} else if err != nil || response == nil || response.Output.Message.Text() != "ready" {
					t.Fatalf("stream=%t: response=%v error=%v", stream, response, err)
				}
			}
			if !test.unsupported && calls.Load() != 2 {
				t.Fatalf("provider calls = %d, want 2", calls.Load())
			}
		})
	}
}
