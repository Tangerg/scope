package openai_test

import (
	"net/http"
	"testing"

	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestMetadataCannotReplaceBoundOpenAICredentials(t *testing.T) {
	for _, name := range []string{"Authorization", "authorization", "aUtHoRiZaTiOn", "Api-Key", "X-Api-Key", "Proxy-Authorization", "Cookie"} {
		for _, values := range [][]string{nil, {"Bearer bound-key"}, {"competing-key"}} {
			t.Run(name, func(t *testing.T) {
				transport := new(bindingTransport)
				client := &http.Client{Transport: transport}
				headers := http.Header{name: values}
				chatModel, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "bound-key", BaseURL: "https://api.example", HTTPClient: client, Headers: headers}, testChatDialect())
				if err == nil || chatModel != nil {
					t.Fatalf("chat accepted credential metadata: model=%v error=%v", chatModel, err)
				}
				responses, err := openai.NewResponses(t.Context(), openai.ResponsesConfig{APIKey: "bound-key", BaseURL: "https://api.example", HTTPClient: client, Headers: headers}, testResponsesDialect())
				if err == nil || responses != nil {
					t.Fatalf("Responses accepted credential metadata: model=%v error=%v", responses, err)
				}
				if transport.calls != 0 {
					t.Fatalf("rejected binding made %d calls", transport.calls)
				}
			})
		}
	}
}

func TestMetadataRejectsAmbiguousHeaderCasing(t *testing.T) {
	_, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{
		APIKey: "key", BaseURL: "https://api.example", Headers: http.Header{"X-App": {"one"}, "x-app": {"two"}},
	}, testChatDialect())
	if err == nil {
		t.Fatal("duplicate canonical metadata header was accepted")
	}
}
