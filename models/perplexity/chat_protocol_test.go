package perplexity_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", `{"id":"sonar-1","object":"chat.completion.chunk","created":1770000000,"model":"sonar-pro","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"answer"}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"search_context_size":"high","cost":{"total_cost":0.02}},"citations":["https://example.com/source"],"search_results":[{"title":"Source","url":"https://example.com/source","source":"web"}]}`)
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
	raw, found, err := response.Metadata.Extra.Decode[map[string]any](perplexity.OpenAIStreamChunkExtensionKey)
	if err != nil {
		t.Fatalf("decode response extension: %v", err)
	} else if !found {
		t.Fatal("response extension not found")
	}
	if citations, ok := raw["citations"].([]any); !ok || len(citations) != 1 || citations[0] != "https://example.com/source" {
		t.Fatalf("citations = %#v; raw = %#v", raw["citations"], raw)
	}
}

func TestChatCallSupportsProSearch(t *testing.T) {
	var wire map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Error(err)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"id\":\"search\",\"model\":\"sonar-pro\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	model, err := perplexity.NewChat(t.Context(), perplexity.ChatConfig{APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: perplexity.ModelSonarPro}})
	if err != nil {
		t.Fatal(err)
	}
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("question"))}}
	if setErr := request.Options.Extensions.Set(perplexity.RequestExtensionKey, perplexity.RequestOptions{WebSearchOptions: &perplexity.WebSearchOptions{SearchType: perplexity.SearchTypePro}}); setErr != nil {
		t.Fatal(setErr)
	}
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if wire["stream"] != true || response.Output.Message.Text() != "answer" {
		t.Fatalf("wire = %v, response = %v", wire, response)
	}
}

func TestChatRejectsUnsupportedSearchBeforeProviderIO(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		t.Error("invalid search options reached the provider")
		http.Error(writer, "unexpected request", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	for _, test := range []struct {
		name, model string
		searchType  perplexity.SearchType
		want        string
	}{
		{name: "unsupported model", model: perplexity.ModelSonar, searchType: perplexity.SearchTypePro, want: "search_type is supported only by model"},
		{name: "unknown search type", model: perplexity.ModelSonarPro, searchType: "invalid", want: "search_type has unsupported value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model, err := perplexity.NewChat(t.Context(), perplexity.ChatConfig{APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: test.model}})
			if err != nil {
				t.Fatal(err)
			}
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("question"))}}
			if setErr := request.Options.Extensions.Set(perplexity.RequestExtensionKey, perplexity.RequestOptions{WebSearchOptions: &perplexity.WebSearchOptions{SearchType: test.searchType}}); setErr != nil {
				t.Fatal(setErr)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Call = %v, %v; want nil, %q", response, err, test.want)
			}
		})
	}
}
