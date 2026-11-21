package minimax_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/minimax"
)

func TestIgnoredOptionsAreRejectedBeforeProviderIO(t *testing.T) {
	value, topK := 0.5, int64(10)
	// declared marks an option the dialect itself refuses, so a default carrying
	// it fails construction; request-level hooks still refuse the others per call.
	tests := []struct {
		name     string
		options  corechat.Options
		messages bool
		declared bool
	}{
		{name: "chat temperature", options: corechat.Options{Temperature: &value}, declared: true},
		{name: "chat top_p", options: corechat.Options{TopP: &value}, declared: true},
		{name: "chat frequency_penalty", options: corechat.Options{FrequencyPenalty: &value}, declared: true},
		{name: "chat presence_penalty", options: corechat.Options{PresencePenalty: &value}, declared: true},
		{name: "messages temperature", options: corechat.Options{Temperature: &value}, messages: true, declared: true},
		{name: "messages top_p", options: corechat.Options{TopP: &value}, messages: true, declared: true},
		{name: "messages top_k", options: corechat.Options{TopK: &topK}, messages: true, declared: true},
		{name: "messages stop", options: corechat.Options{Stop: []string{"stop"}}, messages: true},
		{name: "chat reasoning effort on M3", options: corechat.Options{ReasoningEffort: "high"}},
		{name: "messages reasoning effort on M3", options: corechat.Options{ReasoningEffort: "high"}, messages: true},
	}
	for _, test := range tests {
		for _, scope := range []struct {
			name     string
			defaults bool
		}{{name: "request"}, {name: "defaults", defaults: true}} {
			t.Run(test.name+"/"+scope.name, func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					http.Error(w, "unexpected request", http.StatusBadRequest)
				}))
				defer server.Close()
				configured := corechat.Options{Model: minimax.ModelM3}
				requestOptions := test.options
				if scope.defaults {
					configured = test.options
					configured.Model = minimax.ModelM3
					requestOptions = corechat.Options{}
				}
				var model corechat.Model
				var err error
				if test.messages {
					model, err = minimax.NewMessages(t.Context(), minimax.MessagesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: configured})
				} else {
					model, err = minimax.NewChatCompletions(t.Context(), minimax.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: configured})
				}
				if scope.defaults && test.declared {
					if err == nil {
						t.Fatal("construction accepted defaults no request could send")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}, Options: requestOptions}
				response, err := model.Call(t.Context(), request)
				if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
					t.Fatalf("Call response=%v, error=%v", response, err)
				}
				streamer := model.(corechat.Streamer)
				for delta, err := range streamer.Stream(t.Context(), request) {
					if delta != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
						t.Fatalf("Stream delta=%v, error=%v", delta, err)
					}
				}
				if calls.Load() != 0 {
					t.Fatalf("provider calls = %d", calls.Load())
				}
			})
		}
	}
}
