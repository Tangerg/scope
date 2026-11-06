package openai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestReasoningDetailsReplayOnlyIssuerPlainText(t *testing.T) {
	for _, test := range []struct {
		name     string
		issuer   string
		field    openai.TextReasoningField
		state    []byte
		wantText string
		reject   bool
	}{
		{name: "own field", issuer: "provider", field: openai.TextReasoning, wantText: "thinking"},
		{name: "foreign provider", issuer: "other", field: openai.TextReasoning},
		{name: "foreign provider and field", issuer: "other", field: openai.TextReasoningContent},
		{name: "unowned visible reasoning"},
		{name: "foreign opaque state", state: []byte("foreign-signature")},
		{name: "own incompatible field", issuer: "provider", field: openai.TextReasoningContent, reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			captured := make(chan []map[string]json.RawMessage, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
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
				fmt.Fprint(writer, "data: {\"id\":\"response\",\"model\":\"model\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
			}))
			t.Cleanup(server.Close)
			model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{
				APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: chat.Options{Model: "model"},
			}, openai.DialectConfig{Provider: "provider", TokenLimitField: openai.TokenLimitMaxTokens, ReasoningDetails: &openai.ReasoningDetailsConfig{
				TextField: "reasoning", DetailsField: "reasoning_details", ReplayPlainText: true,
			}})
			if err != nil {
				t.Fatal(err)
			}
			part := chat.NewReasoningPart("thinking", test.state)
			if test.issuer != "" {
				part, err = openai.NewTextReasoningPart(test.issuer, test.field, "thinking")
				if err != nil {
					t.Fatal(err)
				}
			}
			assistant := chat.NewAssistantMessage(part, chat.NewTextPart("visible answer"))
			before := assistant.Clone()
			response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{
				chat.NewUserMessage(chat.NewTextPart("question")), assistant,
			}})
			if test.reject {
				if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
					t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
				}
				return
			}
			if err != nil || calls.Load() != 1 {
				t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
			}
			messages := <-captured
			if len(messages) != 2 {
				t.Fatalf("wire messages=%v", messages)
			}
			raw, exists := messages[1]["reasoning"]
			if exists != (test.wantText != "") {
				t.Fatalf("reasoning replay presence=%v, want %v", exists, test.wantText != "")
			}
			if exists {
				var got string
				if err := jsonv2.Unmarshal(raw, &got); err != nil || got != test.wantText {
					t.Fatalf("reasoning replay=%q, error=%v, want %q", got, err, test.wantText)
				}
			}
			var content string
			if err := jsonv2.Unmarshal(messages[1]["content"], &content); err != nil || content != "visible answer" {
				t.Fatalf("visible history=%q, error=%v", content, err)
			}
			if !reflect.DeepEqual(assistant, before) {
				t.Fatal("replay mutated the visible reasoning history")
			}
		})
	}
}
