package deepseek_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/models/deepseek"
)

func TestTopPUsesResolvedModelAndThinkingMode(t *testing.T) {
	for _, test := range []struct {
		name     string
		defaults chat.Options
		request  chat.Options
		wantErr  string
	}{
		{name: "Flash defaults with Pro non-thinking override", defaults: chat.Options{Model: deepseek.ModelFlash, ReasoningEffort: "high", TopP: new(0.99)}, request: chat.Options{Model: deepseek.ModelV4Pro, ReasoningEffort: "none"}},
		{name: "Pro defaults with Flash thinking override", defaults: chat.Options{Model: deepseek.ModelV4Pro, ReasoningEffort: "none", TopP: new(0.8)}, request: chat.Options{Model: deepseek.ModelFlash, ReasoningEffort: "high"}, wantErr: "must be at least"},
		{name: "Flash defaults with Pro thinking override", defaults: chat.Options{Model: deepseek.ModelFlash, ReasoningEffort: "high", TopP: new(0.99)}, request: chat.Options{Model: deepseek.ModelV4Pro}, wantErr: "has no effect"},
		{name: "Pro thinking overrides non-thinking default", defaults: chat.Options{Model: deepseek.ModelV4Pro, ReasoningEffort: "none", TopP: new(0.8)}, request: chat.Options{ReasoningEffort: "high"}, wantErr: "has no effect"},
		{name: "Pro supplied only at call", defaults: chat.Options{TopP: new(0.8)}, request: chat.Options{Model: deepseek.ModelV4Pro, ReasoningEffort: "none"}},
		{name: "Flash supplied only at call", defaults: chat.Options{TopP: new(0.99)}, request: chat.Options{Model: deepseek.ModelFlash, ReasoningEffort: "high"}},
		{name: "unknown model override", defaults: chat.Options{Model: deepseek.ModelFlash, TopP: new(0.99)}, request: chat.Options{Model: "unknown-model"}, wantErr: "sampling policy is unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				var body struct {
					Model           string  `json:"model"`
					ReasoningEffort string  `json:"reasoning_effort"`
					TopP            float64 `json:"top_p"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
				}
				if body.Model != test.request.Model || body.ReasoningEffort != string(test.request.ReasoningEffort) || body.TopP != *test.defaults.TopP {
					t.Errorf("request=%+v, want model=%s effort=%s top_p=%g", body, test.request.Model, test.request.ReasoningEffort, *test.defaults.TopP)
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(writer, "data: {\"id\":\"chat-1\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", body.Model)
			}))
			t.Cleanup(server.Close)
			model, err := deepseek.NewChatCompletions(t.Context(), deepseek.ChatCompletionsConfig{APIKey: "test-key", BaseURL: server.URL, DefaultOptions: test.defaults})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}, Options: test.request})
			if test.wantErr != "" {
				if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || !strings.Contains(err.Error(), test.wantErr) || calls.Load() != 0 {
					t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
				}
				return
			}
			if err != nil || response.Text() != "answer" || calls.Load() != 1 {
				t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
			}
		})
	}
}

func TestRequestOptionsRejectForcedToolChoiceWhileThinking(t *testing.T) {
	tool := toolcontract.Definition{Name: "lookup", Description: "Look up a value.", InputSchema: json.RawMessage(`{"type":"object"}`)}
	request := &chat.Request{
		Messages:   []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))},
		Tools:      []toolcontract.Definition{tool},
		ToolChoice: &chat.ToolChoice{Mode: chat.ToolChoiceRequired},
		Options:    chat.Options{Model: deepseek.ModelFlash, ReasoningEffort: "high"},
	}
	if err := (deepseek.RequestOptions{}).ValidateFor(request, false); err == nil || !strings.Contains(err.Error(), "tool_choice") {
		t.Fatalf("ValidateFor error = %v, want a tool_choice rejection", err)
	}
	request.Options.ReasoningEffort = "none"
	if err := (deepseek.RequestOptions{}).ValidateFor(request, false); err != nil {
		t.Fatalf("ValidateFor without thinking = %v", err)
	}
}

func TestRequestOptionsBoundEachStopSequence(t *testing.T) {
	request := &chat.Request{Options: chat.Options{Stop: []string{strings.Repeat("x", 512)}}}
	if err := (deepseek.RequestOptions{}).ValidateFor(request, false); err != nil {
		t.Fatalf("512-byte stop sequence = %v", err)
	}
	request.Options.Stop = []string{strings.Repeat("x", 513)}
	if err := (deepseek.RequestOptions{}).ValidateFor(request, false); err == nil || !strings.Contains(err.Error(), "options.stop[0]") {
		t.Fatalf("513-byte stop sequence = %v, want a refusal", err)
	}
}
