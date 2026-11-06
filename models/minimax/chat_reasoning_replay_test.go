package minimax_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/models/minimax"
)

func TestChatReplaysPlainReasoningAfterToolExecution(t *testing.T) {
	var calls atomic.Int64
	captured := make(chan []map[string]json.RawMessage, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		captured <- body.Messages
		writer.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			fmt.Fprint(writer, "data: {\"id\":\"first\",\"model\":\"MiniMax-M3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"I need\"}}]}\n\n")
			fmt.Fprint(writer, "data: {\"id\":\"first\",\"model\":\"MiniMax-M3\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\" a lookup.\"}}]}\n\n")
			fmt.Fprint(writer, "data: {\"id\":\"first\",\"model\":\"MiniMax-M3\",\"choices\":[{\"index\":0,\"finish_reason\":\"tool_calls\",\"delta\":{\"tool_calls\":[{\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"},\"index\":0}]}}]}\n\n")
		} else {
			fmt.Fprint(writer, "data: {\"id\":\"second\",\"model\":\"MiniMax-M3\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\"}}]}\n\n")
		}
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	model, err := minimax.NewChatCompletions(t.Context(), minimax.ChatCompletionsConfig{
		APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: corechat.Options{Model: minimax.ModelM3},
	})
	if err != nil {
		t.Fatal(err)
	}
	user := corechat.NewUserMessage(corechat.NewTextPart("look up"))
	response, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{user}})
	if err != nil {
		t.Fatal(err)
	}
	<-captured
	if response.Output.Message == nil {
		t.Fatal("missing assistant message")
	}
	raw, err := response.Output.Message.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var assistant corechat.Message
	if err := assistant.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	if len(assistant.Parts) != 2 || assistant.Parts[0].Kind != corechat.PartReasoning || assistant.Parts[0].Text != "I need a lookup." || len(assistant.Parts[0].ReasoningState) != 0 {
		t.Fatalf("stored assistant=%+v", assistant)
	}
	if _, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		user, assistant,
		corechat.NewToolMessage(corechat.ToolResult{ID: "call-1", Name: "lookup", Output: toolcontract.NewTextOutput("found")}),
	}}); err != nil {
		t.Fatal(err)
	}
	messages := <-captured
	if len(messages) != 3 {
		t.Fatalf("wire messages=%v", messages)
	}
	var reasoning string
	if err := jsonv2.Unmarshal(messages[1]["reasoning_content"], &reasoning); err != nil {
		t.Fatal(err)
	}
	if reasoning != assistant.Parts[0].Text {
		t.Fatalf("replayed reasoning=%q, want %q", reasoning, assistant.Parts[0].Text)
	}
	if _, exists := messages[1]["reasoning_details"]; exists {
		t.Fatalf("plain reasoning fabricated structured state: %v", messages[1])
	}
	if calls.Load() != 2 {
		t.Fatalf("provider calls=%d, want 2", calls.Load())
	}
}
