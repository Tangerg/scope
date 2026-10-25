package minimax_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/minimax"
)

func TestIgnoredOptionsAreRejectedBeforeProviderIO(t *testing.T) {
	value, topK := 0.5, int64(10)
	tests := []struct {
		name     string
		options  corechat.Options
		messages bool
	}{
		{name: "chat temperature", options: corechat.Options{Temperature: &value}},
		{name: "chat top_p", options: corechat.Options{TopP: &value}},
		{name: "chat frequency_penalty", options: corechat.Options{FrequencyPenalty: &value}},
		{name: "chat presence_penalty", options: corechat.Options{PresencePenalty: &value}},
		{name: "messages temperature", options: corechat.Options{Temperature: &value}, messages: true},
		{name: "messages top_p", options: corechat.Options{TopP: &value}, messages: true},
		{name: "messages top_k", options: corechat.Options{TopK: &topK}, messages: true},
		{name: "messages stop", options: corechat.Options{Stop: []string{"stop"}}, messages: true},
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
				if err != nil {
					t.Fatal(err)
				}
				request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}, Options: requestOptions}
				response, err := model.Call(t.Context(), request)
				if response != nil || corechat.ClassifyFailure(err) != corechat.CallFailureNotExecuted {
					t.Fatalf("Call response=%v, error=%v", response, err)
				}
				streamer := model.(corechat.Streamer)
				for delta, err := range streamer.Stream(t.Context(), request) {
					if delta != nil || corechat.ClassifyFailure(err) != corechat.CallFailureNotExecuted {
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
