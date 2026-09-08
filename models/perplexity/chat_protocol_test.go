package perplexity_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/perplexity"
)

func TestChatMapsOfficialSonarOptionsAndResponse(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			t.Errorf("path = %q; want official OpenAI-compatible alias /chat/completions", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"sonar-1",
			"object":"chat.completion",
			"created":1770000000,
			"model":"sonar-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"search_context_size":"high","cost":{"total_cost":0.02}},
			"citations":["https://example.com/source"],
			"search_results":[{"title":"Source","url":"https://example.com/source","source":"web"}]
		}`))
	}))
	t.Cleanup(server.Close)

	model, err := perplexity.NewChat(t.Context(), perplexity.ChatConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		DefaultOptions: corechat.Options{
			Model: perplexity.ModelSonarPro,
		},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	request := &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("question")),
	}}
	format, err := corechat.NewOutputFormat(corechat.OutputFormatText)
	if err != nil {
		t.Fatal(err)
	}
	request.Options.OutputFormat = &format
	returnImages := true
	disableSearch := false
	if setExtensionErr := request.Options.Extensions.Set(perplexity.RequestExtensionKey, perplexity.RequestOptions{
		SearchMode:            perplexity.SearchModeWeb,
		ReturnImages:          &returnImages,
		DisableSearch:         &disableSearch,
		SearchDomainFilter:    []string{"example.com"},
		SearchLanguageFilter:  []string{"en"},
		ImageFormatFilter:     []perplexity.ImageFormat{perplexity.ImageFormatPNG},
		ImageDomainFilter:     []string{"images.example.com"},
		LanguagePreference:    "en",
		SearchAfterDateFilter: perplexity.SearchDate("01/01/2026"),
		WebSearchOptions:      &perplexity.WebSearchOptions{SearchContextSize: perplexity.SearchContextHigh, SearchType: perplexity.SearchTypeFast},
	}); setExtensionErr != nil {
		t.Fatalf("SetExtension: %v", setExtensionErr)
	}

	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if body["search_mode"] != "web" || body["language_preference"] != "en" {
		t.Fatalf("Sonar fields missing from request: %#v", body)
	}
	webOptions, ok := body["web_search_options"].(map[string]any)
	if !ok || webOptions["search_context_size"] != "high" || webOptions["search_type"] != "fast" {
		t.Fatalf("web_search_options = %#v", body["web_search_options"])
	}
	raw, found, err := response.Metadata.Extra.Decode[map[string]any](perplexity.OpenAIResponseExtensionKey)
	if err != nil {
		t.Fatalf("decode response extension: %v", err)
	} else if !found {
		t.Fatal("response extension not found")
	}
	if citations, ok := raw["citations"].([]any); !ok || len(citations) != 1 || citations[0] != "https://example.com/source" {
		t.Fatalf("citations = %#v; raw = %#v", raw["citations"], raw)
	}
}

func TestChatRejectsProSearchWithoutStreaming(t *testing.T) {
	model, err := perplexity.NewChat(t.Context(), perplexity.ChatConfig{
		APIKey:         "test-key",
		DefaultOptions: corechat.Options{Model: perplexity.ModelSonarPro},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	request := &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("question")),
	}}
	if err := request.Options.Extensions.Set(perplexity.RequestExtensionKey, perplexity.RequestOptions{
		WebSearchOptions: &perplexity.WebSearchOptions{SearchType: perplexity.SearchTypePro},
	}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	if _, err := model.Call(t.Context(), request); err == nil {
		t.Fatal("Call succeeded; want Pro Search streaming error")
	}
}
