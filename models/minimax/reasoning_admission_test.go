package minimax_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/minimax"
)

func TestM31ReasoningEffortReachesBothNativeProtocols(t *testing.T) {
	for _, messages := range []bool{false, true} {
		for _, effort := range []chat.ReasoningEffort{"low", "medium", "high", "xhigh", "max"} {
			t.Run(fmt.Sprintf("messages=%t/%s", messages, effort), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body struct {
						Model           string `json:"model"`
						ReasoningEffort string `json:"reasoning_effort"`
						OutputConfig    struct {
							Effort string `json:"effort"`
						} `json:"output_config"`
					}
					if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
						t.Error(err)
					}
					got := body.ReasoningEffort
					if messages {
						got = body.OutputConfig.Effort
					}
					if body.Model != minimax.ModelM31Flash || got != string(effort) {
						t.Errorf("request=%+v", body)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if messages {
						fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"MiniMax-M3.1-Flash\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"answer\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					} else {
						fmt.Fprint(w, "data: {\"id\":\"m\",\"model\":\"MiniMax-M3.1-Flash\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
					}
				}))
				defer server.Close()
				defaults := chat.Options{Model: minimax.ModelM31Flash, ReasoningEffort: effort}
				var model chat.Model
				var err error
				if messages {
					model, err = minimax.NewMessages(t.Context(), minimax.MessagesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: defaults})
				} else {
					model, err = minimax.NewChatCompletions(t.Context(), minimax.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: defaults})
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 1 {
					t.Fatalf("calls=%d", calls.Load())
				}
			})
		}
	}
}

func TestM31RejectsIllegalReasoningOptionsBeforeIO(t *testing.T) {
	for _, test := range []struct {
		name    string
		effort  chat.ReasoningEffort
		options minimax.ChatRequestOptions
	}{
		{name: "disabled effort", effort: "none"},
		{name: "minimal effort", effort: "minimal"},
		{name: "disabled thinking", options: minimax.ChatRequestOptions{Thinking: minimax.ThinkingDisabled}},
		{name: "merged reasoning", options: minimax.ChatRequestOptions{ReasoningSplit: new(false)}},
		{name: "service tier", options: minimax.ChatRequestOptions{ServiceTier: minimax.ServiceTierStandard}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadRequest) }))
			defer server.Close()
			model, err := minimax.NewChatCompletions(t.Context(), minimax.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: minimax.ModelM31Flash}})
			if err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}, Options: chat.Options{ReasoningEffort: test.effort}}
			if err := request.Options.Extensions.Set(minimax.RequestExtensionKey, test.options); err != nil {
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

func TestM31MessagesRejectsUnsupportedNativeControlsBeforeIO(t *testing.T) {
	for _, controls := range []map[string]any{
		{"thinking": map[string]any{"type": "disabled"}},
		{"service_tier": "standard"},
	} {
		t.Run(fmt.Sprint(controls), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadRequest) }))
			defer server.Close()
			model, err := minimax.NewMessages(t.Context(), minimax.MessagesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: minimax.ModelM31Flash}})
			if err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
			if err := request.Options.Extensions.Set(minimax.AnthropicRequestExtensionKey, controls); err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), request); invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls.Load() != 0 {
				t.Fatalf("error=%v calls=%d", err, calls.Load())
			}
		})
	}
}
