package openai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	scopeopenai "github.com/Tangerg/scope/models/protocol/openai"
)

type textReasoningDialectCase struct {
	name            string
	dialect         scopeopenai.Dialect
	responseField   string
	wantReplayField string
	withToolCall    bool
}

func TestTextReasoningDialects(t *testing.T) {
	tests := []textReasoningDialectCase{
		{name: "reasoning_content output only", dialect: scopeopenai.ReasoningContentDialect("test"), responseField: "reasoning_content"},
		{name: "reasoning_content full replay", dialect: scopeopenai.ReasoningContentReplayDialect("test"), responseField: "reasoning_content", wantReplayField: "reasoning_content"},
		{name: "reasoning_content tool replay", dialect: scopeopenai.ReasoningContentToolReplayDialect("test"), responseField: "reasoning_content", wantReplayField: "reasoning_content", withToolCall: true},
		{name: "reasoning output only", dialect: scopeopenai.ReasoningDialect("test"), responseField: "reasoning"},
		{name: "reasoning full replay", dialect: scopeopenai.ReasoningReplayDialect("test"), responseField: "reasoning", wantReplayField: "reasoning"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testTextReasoningDialect(t, test)
		})
	}
}

func testTextReasoningDialect(t *testing.T, test textReasoningDialectCase) {
	t.Helper()
	var requestBody struct {
		Messages []map[string]any `json:"messages"`
	}
	server := newTextReasoningServer(t, test.responseField, &requestBody)
	t.Cleanup(server.Close)

	adapter, err := scopeopenai.NewCompatibleChatCompletions(t.Context(), scopeopenai.ChatCompletionsConfig{
		APIKey:         "test-key",
		DefaultOptions: corechat.Options{Model: "provider-model"},
		BaseURL:        server.URL,
	}, test.dialect)
	if err != nil {
		t.Fatalf("NewCompatibleChatCompletions: %v", err)
	}
	field := scopeopenai.TextReasoningContent
	if test.responseField == "reasoning" {
		field = scopeopenai.TextReasoning
	}
	priorReasoning, err := scopeopenai.NewTextReasoningPart("test", field, "prior reasoning")
	if err != nil {
		t.Fatalf("NewTextReasoningPart: %v", err)
	}
	assistantParts := []corechat.Part{priorReasoning, corechat.NewTextPart("prior answer")}
	if test.withToolCall {
		assistantParts = append(assistantParts, corechat.NewToolCallPart(corechat.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{}`}))
	}
	response, err := adapter.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("first")),
		corechat.NewAssistantMessage(assistantParts...),
		corechat.NewUserMessage(corechat.NewTextPart("second")),
	}})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	assertTextReasoningReplay(t, requestBody.Messages[1], test.wantReplayField)
	parts := response.Output.Message.Parts
	if len(parts) != 2 || parts[0].Kind != corechat.PartReasoning || parts[0].Text != "fresh reasoning" || parts[1].Text != "answer" {
		t.Fatalf("response parts = %#v", parts)
	}
}

func newTextReasoningServer(t *testing.T, responseField string, requestBody any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(requestBody); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"id": "chat-1", "model": "provider-model",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "answer", responseField: "fresh reasoning"},
			}},
		}
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
}

func assertTextReasoningReplay(t *testing.T, assistant map[string]any, wantField string) {
	t.Helper()
	for _, field := range []string{"reasoning", "reasoning_content"} {
		value, found := assistant[field]
		if field == wantField && (!found || value != "prior reasoning") {
			t.Errorf("request %s = %#v/%v; want prior reasoning", field, value, found)
		}
		if field != wantField && found {
			t.Errorf("unexpected request field %s = %#v", field, value)
		}
	}
}

func TestChatTokenLimitFieldMatchesProtocol(t *testing.T) {
	tests := []struct {
		name      string
		construct func(context.Context, scopeopenai.ChatCompletionsConfig) (*scopeopenai.ChatCompletions, error)
		wantField string
	}{
		{name: "native", construct: scopeopenai.NewChatCompletions, wantField: "max_completion_tokens"},
		{
			name: "compatible",
			construct: func(ctx context.Context, config scopeopenai.ChatCompletionsConfig) (*scopeopenai.ChatCompletions, error) {
				return scopeopenai.NewCompatibleChatCompletions(ctx, config, scopeopenai.Dialect{Provider: "test", TokenLimitField: scopeopenai.TokenLimitMaxTokens})
			},
			wantField: "max_tokens",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(writer, "invalid request", http.StatusBadRequest)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"id":"chat-1","model":"model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`))
			}))
			t.Cleanup(server.Close)

			maxTokens := int64(321)
			adapter, err := test.construct(t.Context(), scopeopenai.ChatCompletionsConfig{
				APIKey: "test-key",
				DefaultOptions: corechat.Options{
					Model:           "model",
					MaxOutputTokens: &maxTokens,
				},
				BaseURL: server.URL,
			})
			if err != nil {
				t.Fatalf("construct: %v", err)
			}
			if _, err := adapter.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
				corechat.NewUserMessage(corechat.NewTextPart("hello")),
			}}); err != nil {
				t.Fatalf("Call: %v", err)
			}
			if body[test.wantField] != float64(maxTokens) {
				t.Fatalf("%s = %#v; want %d", test.wantField, body[test.wantField], maxTokens)
			}
			otherField := "max_tokens"
			if test.wantField == otherField {
				otherField = "max_completion_tokens"
			}
			if _, found := body[otherField]; found {
				t.Fatalf("unexpected %s in request: %#v", otherField, body)
			}
		})
	}
}

func TestDialectRequiresAnExplicitTokenLimitField(t *testing.T) {
	dialect := scopeopenai.Dialect{Provider: "test"}
	if err := dialect.Validate(); err == nil {
		t.Fatal("Dialect.Validate accepted an implicit token limit field")
	}
	if scopeopenai.TokenLimitField("").Valid() {
		t.Fatal("zero TokenLimitField is valid")
	}
}
