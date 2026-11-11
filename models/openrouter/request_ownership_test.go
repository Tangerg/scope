package openrouter_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/models/openrouter"
)

func TestChatRejectsNativeReasoningEffort(t *testing.T) {
	model, err := openrouter.NewChatCompletions(t.Context(), openrouter.ChatCompletionsConfig{APIKey: "test", BaseURL: "http://127.0.0.1:1", DefaultOptions: chat.Options{Model: "test-model"}})
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
	if err := request.Options.Extensions.Set(openrouter.OpenAIRequestExtensionKey, map[string]any{"reasoning": map[string]any{"effort": "high"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !errors.Is(err, metadata.ErrCoreOwnedField) {
		t.Fatalf("Call=%v", err)
	}
}

func TestReasoningIntentHasOneOwnerBeforeIO(t *testing.T) {
	for _, test := range []struct {
		name      string
		effort    chat.ReasoningEffort
		reasoning map[string]any
	}{
		{"native enabled", "", map[string]any{"enabled": true}},
		{"native disabled", "", map[string]any{"enabled": false}},
		{"conflicting enabled", "none", map[string]any{"enabled": true}},
		{"conflicting disabled", "high", map[string]any{"enabled": false}},
		{"budget and effort", "high", map[string]any{"max_tokens": 1024}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadRequest) }))
			defer server.Close()
			model, err := openrouter.NewChatCompletions(t.Context(), openrouter.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "test-model", ReasoningEffort: test.effort}})
			if err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
			if err := request.Options.Extensions.Set(openrouter.OpenAIRequestExtensionKey, map[string]any{"reasoning": test.reasoning}); err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), request); invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
				t.Fatalf("error=%v", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("calls=%d", calls.Load())
			}
		})
	}
}

func TestIndependentNativeReasoningControlsReachProvider(t *testing.T) {
	for _, effort := range []chat.ReasoningEffort{"", "high"} {
		t.Run(string(effort), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					ReasoningEffort string `json:"reasoning_effort"`
					Reasoning       struct {
						Exclude   bool `json:"exclude"`
						MaxTokens int  `json:"max_tokens"`
					} `json:"reasoning"`
				}
				if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
					t.Error(err)
				}
				if !body.Reasoning.Exclude || body.ReasoningEffort != string(effort) {
					t.Errorf("body=%+v", body)
				}
				if effort == "" && body.Reasoning.MaxTokens != 1024 {
					t.Errorf("budget=%d", body.Reasoning.MaxTokens)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"m\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model, err := openrouter.NewChatCompletions(t.Context(), openrouter.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "test-model", ReasoningEffort: effort}})
			if err != nil {
				t.Fatal(err)
			}
			reasoning := map[string]any{"exclude": true}
			if effort == "" {
				reasoning["max_tokens"] = 1024
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
			if err := request.Options.Extensions.Set(openrouter.OpenAIRequestExtensionKey, map[string]any{"reasoning": reasoning}); err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
		})
	}
}
