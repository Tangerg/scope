package minimax_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/models/minimax"
)

func TestMessagesToolChoiceAdmission(t *testing.T) {
	for _, modelName := range []string{minimax.ModelM3, minimax.ModelM31Flash} {
		for _, choice := range []chat.ToolChoice{{Mode: chat.ToolChoiceRequired}, {Mode: chat.ToolChoiceNamed, Name: "lookup"}, {Mode: chat.ToolChoiceNone}} {
			t.Run(modelName+"/"+string(choice.Mode), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadRequest) }))
				defer server.Close()
				model, err := minimax.NewMessages(t.Context(), minimax.MessagesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: modelName}})
				if err != nil {
					t.Fatal(err)
				}
				request := &chat.Request{
					Messages: []chat.Message{
						chat.NewUserMessage(chat.NewTextPart("lookup")),
						chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "lookup", Arguments: `{}`})),
						chat.NewToolMessage(chat.ToolResult{ID: "call", Name: "lookup", Output: tool.NewTextOutput("found")}),
					},
					Tools:      []tool.Definition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}},
					ToolChoice: &choice,
				}
				if response, err := model.Call(t.Context(), request); response != nil || !errors.Is(err, errors.ErrUnsupported) || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
					t.Fatalf("response=%v error=%v", response, err)
				}
				failures := 0
				for delta, err := range model.Stream(t.Context(), request) {
					if delta != nil || !errors.Is(err, errors.ErrUnsupported) || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
						t.Fatalf("delta=%v error=%v", delta, err)
					}
					failures++
				}
				if calls.Load() != 0 || failures != 1 {
					t.Fatalf("calls=%d failures=%d", calls.Load(), failures)
				}
			})
		}
	}
}

func TestMessagesRetainsAutomaticToolChoice(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			ToolChoice struct {
				Type string `json:"type"`
			} `json:"tool_choice"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
			t.Error(err)
		}
		if body.ToolChoice.Type != "auto" {
			t.Errorf("tool choice=%q", body.ToolChoice.Type)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"MiniMax-M3\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"answer\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	model, err := minimax.NewMessages(t.Context(), minimax.MessagesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: minimax.ModelM3}})
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("lookup"))}, Tools: []tool.Definition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}, ToolChoice: &chat.ToolChoice{Mode: chat.ToolChoiceAuto}}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for _, err := range model.Stream(t.Context(), request) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestChatRequestsAndPreservesTrailingUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream        bool `json:"stream"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
			t.Error(err)
		}
		if !body.Stream || !body.StreamOptions.IncludeUsage {
			t.Errorf("request=%+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"m\",\"model\":\"MiniMax-M3\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\"}}]}\n\ndata: {\"id\":\"m\",\"model\":\"MiniMax-M3\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model, err := minimax.NewChatCompletions(t.Context(), minimax.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: minimax.ModelM3}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
	if err != nil {
		t.Fatal(err)
	}
	usage := response.Metadata.Usage
	if usage == nil || usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("usage=%+v", usage)
	}
}
