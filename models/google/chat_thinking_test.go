package google_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

func TestChatAdmitsOneThinkingControlAfterDefaults(t *testing.T) {
	for _, binding := range []struct{ provider, requestKey string }{
		{provider: google.Provider, requestKey: google.RequestExtensionKey},
		{provider: vertexai.Provider, requestKey: vertexai.RequestExtensionKey},
	} {
		for _, test := range []struct {
			name          string
			defaultEffort chat.ReasoningEffort
			requestEffort chat.ReasoningEffort
			defaultNative string
			requestNative string
			want          *genai.ThinkingConfig
			conflict      bool
		}{
			{name: "absent"},
			{name: "level with thoughts", requestEffort: "high", requestNative: `{"thinkingConfig":{"includeThoughts":true}}`, want: &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelHigh, IncludeThoughts: true}},
			{name: "budget with thoughts", requestNative: `{"thinkingConfig":{"thinkingBudget":1024,"includeThoughts":true}}`, want: &genai.ThinkingConfig{ThinkingBudget: new(int32(1024)), IncludeThoughts: true}},
			{name: "default level with request budget", defaultEffort: "high", requestNative: `{"thinkingConfig":{"thinkingBudget":1024}}`, conflict: true},
			{name: "default budget with request level", defaultNative: `{"thinkingConfig":{"thinkingBudget":1024}}`, requestEffort: "high", conflict: true},
			{name: "request level and budget", requestEffort: "high", requestNative: `{"thinkingConfig":{"thinkingBudget":1024}}`, conflict: true},
			{name: "default level and budget", defaultEffort: "high", defaultNative: `{"thinkingConfig":{"thinkingBudget":1024}}`, conflict: true},
			{name: "zero budget is explicit", requestEffort: "low", requestNative: `{"thinkingConfig":{"thinkingBudget":0}}`, conflict: true},
			{name: "dynamic budget is explicit", requestEffort: "low", requestNative: `{"thinkingConfig":{"thinkingBudget":-1}}`, conflict: true},
			{name: "null budget is absent", requestEffort: "low", requestNative: `{"thinkingConfig":{"thinkingBudget":null}}`, want: &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelLow}},
		} {
			t.Run(binding.provider+"/"+test.name, func(t *testing.T) {
				var calls atomic.Int64
				captured := make(chan *genai.ThinkingConfig, 1)
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls.Add(1)
					var body struct {
						GenerationConfig struct {
							ThinkingConfig *genai.ThinkingConfig `json:"thinkingConfig"`
						} `json:"generationConfig"`
					}
					if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
						t.Error(err)
						http.Error(writer, "invalid request", http.StatusBadRequest)
						return
					}
					captured <- body.GenerationConfig.ThinkingConfig
					writer.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(writer, "data: {\"responseId\":\"response\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"STOP\"}]}\n\n")
				}))
				t.Cleanup(server.Close)
				defaults := chat.Options{Model: "test-model", ReasoningEffort: test.defaultEffort}
				request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}, Options: chat.Options{ReasoningEffort: test.requestEffort}}
				if test.defaultNative != "" {
					if err := defaults.Extensions.Set(binding.requestKey, json.RawMessage(test.defaultNative)); err != nil {
						t.Fatal(err)
					}
				}
				if test.requestNative != "" {
					if err := request.Options.Extensions.Set(binding.requestKey, json.RawMessage(test.requestNative)); err != nil {
						t.Fatal(err)
					}
				}
				var model chat.Model
				var err error
				if binding.provider == google.Provider {
					model, err = google.NewChat(t.Context(), google.ChatConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client(), DefaultOptions: defaults})
				} else {
					model, err = vertexai.NewChat(t.Context(), vertexai.ChatConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()}, DefaultOptions: defaults})
				}
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if test.conflict {
					if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || !strings.Contains(err.Error(), "mutually exclusive") || calls.Load() != 0 {
						t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
					}
					return
				}
				if err != nil || calls.Load() != 1 {
					t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
				}
				if got := <-captured; !reflect.DeepEqual(got, test.want) {
					t.Fatalf("thinking config=%+v, want %+v", got, test.want)
				}
			})
		}
	}
}
