package perplexity_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/perplexity"
)

func TestAgentResponsesPreservesNativeSearchAndUsage(t *testing.T) {
	var wire map[string]json.RawMessage
	search := `{"type":"search_results","results":[{"id":9007199254740993,"url":"https://example.com/source","snippet":"source"}]}`
	terminal := `{"id":"response-1","model":"openai/gpt-6-sol","status":"completed","output":[` + search + `,{"type":"message","id":"message-1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":0,"output_tokens":2,"total_tokens":2,"cost":{"total_cost":0.00001}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if err := jsonv2.UnmarshalRead(r.Body, &wire); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{`{"type":"response.created","response":{"id":"response-1","model":"openai/gpt-6-sol","status":"in_progress"}}`, `{"type":"response.output_item.added","output_index":0,"item":` + search + `}`, `{"type":"response.agent_step.completed","step":9007199254740993}`, `{"type":"response.output_text.delta","item_id":"message-1","output_index":1,"content_index":0,"delta":"answer"}`, `{"type":"response.completed","response":` + terminal + `}`} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	model, err := perplexity.NewResponses(t.Context(), perplexity.ResponsesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "openai/gpt-6-sol"}})
	if err != nil {
		t.Fatal(err)
	}
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("question"))}, Options: corechat.Options{ReasoningEffort: "low"}}
	request.Tools = []corechat.ToolDefinition{{Name: "local", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	if setErr := request.Options.Extensions.Set(perplexity.RequestExtensionKey, perplexity.RequestOptions{Effort: "high@latest", MaxSteps: new(int64(4)), Store: new(false), Tools: []json.RawMessage{json.RawMessage(`{"type":"web_search","filters":{"search_domain_filter":["example.com"]}}`)}}); setErr != nil {
		t.Fatal(setErr)
	}
	var accumulator corechat.ResponseAccumulator
	seenSearch, seenUnknown := false, false
	for delta, err := range model.Stream(t.Context(), request) {
		if err != nil {
			t.Fatal(err)
		}
		if err := accumulator.Add(delta); err != nil {
			t.Fatal(err)
		}
		raw := string(delta.Metadata.Extra[perplexity.StreamEventExtensionKey])
		seenSearch = seenSearch || strings.Contains(raw, `"search_results"`)
		seenUnknown = seenUnknown || strings.Contains(raw, `"response.agent_step.completed"`)
	}
	response, err := accumulator.Response()
	if err != nil {
		t.Fatal(err)
	}
	if response.Output.Message.Text() != "answer" || !seenSearch || !seenUnknown {
		t.Fatalf("response=%v search=%t unknown=%t", response, seenSearch, seenUnknown)
	}
	if response.Metadata.Usage == nil || response.Metadata.Usage.InputTokens != 0 || response.Metadata.Usage.OutputTokens != 2 {
		t.Fatal(response.Metadata.Usage)
	}
	if !strings.Contains(string(response.Metadata.Extra[perplexity.ResponseExtensionKey]), `9007199254740993`) {
		t.Fatalf("native response=%s", response.Metadata.Extra[perplexity.ResponseExtensionKey])
	}
	if string(wire["effort"]) != `"high@latest"` || string(wire["max_steps"]) != "4" || string(wire["store"]) != "false" || !strings.Contains(string(wire["reasoning"]), `"effort":"low"`) {
		t.Fatalf("wire=%v", wire)
	}
	if _, exists := wire["include"]; exists {
		t.Fatalf("OpenAI-only include reached Agent API: %s", wire["include"])
	}
	var tools []struct {
		Type string `json:"type"`
	}
	if err := jsonv2.Unmarshal(wire["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Type != "function" || tools[1].Type != "web_search" {
		t.Fatalf("tools=%s", wire["tools"])
	}
	if _, exists := any(model).(interface {
		CountInputTokens(ctx context.Context, req *corechat.Request) (int64, error)
	}); exists {
		t.Fatal("unsupported token count capability")
	}
}
func TestAgentResponsesRejectsSonarAndDuplicateOwnersBeforeIO(t *testing.T) {
	for _, options := range []json.RawMessage{json.RawMessage(`{"search_mode":"web"}`), json.RawMessage(`{"reasoning_effort":"high"}`), json.RawMessage(`{"model":"sonar"}`), json.RawMessage(`{"models":["openai/gpt-6-sol"]}`), json.RawMessage(`{"tools":[{"type":"function","name":"local"}]}`), json.RawMessage(`{"effort":"unknown"}`)} {
		t.Run(string(options), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected", 400) }))
			defer server.Close()
			model, err := perplexity.NewResponses(t.Context(), perplexity.ResponsesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "openai/gpt-6-sol"}})
			if err != nil {
				t.Fatal(err)
			}
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("question"))}}
			if setErr := request.Options.Extensions.Set(perplexity.RequestExtensionKey, options); setErr != nil {
				t.Fatal(setErr)
			}
			_, err = model.Call(t.Context(), request)
			if corechat.ClassifyFailure(err) != corechat.CallFailureNotExecuted || calls != 0 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}

func TestAgentResponsesRequiresCoreModelBeforeIO(t *testing.T) {
	calls := 0
	server := modeltest.JSONServer(http.StatusBadRequest, `{}`, func(*http.Request) { calls++ })
	defer server.Close()
	model, err := perplexity.NewResponses(t.Context(), perplexity.ResponsesConfig{APIKey: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("question"))}}
	if setErr := request.Options.Extensions.Set(perplexity.RequestExtensionKey, perplexity.RequestOptions{Effort: "high"}); setErr != nil {
		t.Fatal(setErr)
	}
	_, err = model.Call(t.Context(), request)
	if corechat.ClassifyFailure(err) != corechat.CallFailureNotExecuted || !strings.Contains(err.Error(), "model is required") || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestAgentResponsesPreservesExplicitEmptyTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire map[string]json.RawMessage
		if err := jsonv2.UnmarshalRead(r.Body, &wire); err != nil {
			t.Error(err)
		}
		if string(wire["tools"]) != `[]` {
			t.Errorf("tools=%s, want explicit empty array", wire["tools"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-1\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	model, err := perplexity.NewResponses(t.Context(), perplexity.ResponsesConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "openai/gpt-6-sol"}})
	if err != nil {
		t.Fatal(err)
	}
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("question"))}}
	if setErr := request.Options.Extensions.Set(perplexity.RequestExtensionKey, perplexity.RequestOptions{Tools: []json.RawMessage{}}); setErr != nil {
		t.Fatal(setErr)
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestAgentResponsesListsModels(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, `{"object":"list","data":[{"id":"openai/gpt-6-sol"},{"id":"anthropic/claude-sonnet-4-6"}]}`, func(request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/models" {
			t.Errorf("request=%s %s", request.Method, request.URL.Path)
		}
	})
	defer server.Close()
	model, err := perplexity.NewResponses(t.Context(), perplexity.ResponsesConfig{APIKey: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	models, err := model.ListModels(t.Context(), 2, 1024)
	if err != nil || len(models) != 2 || models[0] != "openai/gpt-6-sol" || models[1] != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("models=%v error=%v", models, err)
	}
}
