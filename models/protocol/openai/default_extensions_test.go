package openai

import (
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestChatDefaultsReachNativeExtensionAndDialect(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "request"}[override], func(t *testing.T) {
			defaults := corechat.Options{Model: "test"}
			if err := defaults.Extensions.Set(RequestExtensionKey, map[string]any{"service_tier": "flex"}); err != nil {
				t.Fatal(err)
			}
			if err := defaults.Extensions.Set("test/typed", "default"); err != nil {
				t.Fatal(err)
			}
			request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
			if err != nil {
				t.Fatal(err)
			}
			wantTier, wantTyped := "flex", "default"
			if override {
				wantTier, wantTyped = "priority", "override"
				if setErr := request.Options.Extensions.Set(RequestExtensionKey, map[string]any{"service_tier": wantTier}); setErr != nil {
					t.Fatal(setErr)
				}
				if setErr := request.Options.Extensions.Set("test/typed", wantTyped); setErr != nil {
					t.Fatal(setErr)
				}
			}
			before, err := jsonv2.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			model, err := NewCompatibleChatCompletions(t.Context(), ChatCompletionsConfig{APIKey: "test", DefaultOptions: defaults}, Dialect{Provider: "openai", TokenLimitField: TokenLimitMaxCompletionTokens, PrepareRequest: func(source *corechat.Request, _ *CompatibleRequest) error {
				called = true
				value, found, decodeErr := source.Options.Extensions.Decode[string]("test/typed")
				if decodeErr != nil || !found || value != wantTyped {
					t.Errorf("typed extension = %q, %t, %v", value, found, decodeErr)
				}
				return decodeErr
			}})
			if err != nil {
				t.Fatal(err)
			}
			parameters, err := model.buildRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := jsonv2.Marshal(parameters)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				ServiceTier string `json:"service_tier"`
			}
			if decodeErr := jsonv2.Unmarshal(body, &wire); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if wire.ServiceTier != wantTier || !called {
				t.Fatalf("wire tier=%q dialect=%t", wire.ServiceTier, called)
			}
			after, err := jsonv2.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("caller request mutated")
			}
		})
	}
}

func TestCompatibleRequestCannotOverwriteCoreFields(t *testing.T) {
	for _, field := range []string{"model", "messages", "tools", "max_tokens", "max_completion_tokens", "temperature", "top_p", "reasoning_effort", "response_format", "tool_choice", "parallel_tool_calls", "stream"} {
		t.Run(field, func(t *testing.T) {
			model, err := NewCompatibleChatCompletions(t.Context(), ChatCompletionsConfig{APIKey: "test", DefaultOptions: corechat.Options{Model: "test-model"}}, Dialect{Provider: "test", TokenLimitField: TokenLimitMaxTokens, PrepareRequest: func(_ *corechat.Request, target *CompatibleRequest) error { return target.SetExtraField(field, nil) }})
			if err != nil {
				t.Fatal(err)
			}
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
			if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "owned by Core") {
				t.Fatalf("Call=%v", err)
			}
		})
	}
}

func TestDisabledRawExtensionIsRejected(t *testing.T) {
	model, err := NewCompatibleChatCompletions(t.Context(), ChatCompletionsConfig{APIKey: "test", DefaultOptions: corechat.Options{Model: "test-model"}}, Dialect{Provider: "test", TokenLimitField: TokenLimitMaxTokens, DisableRawRequestExtension: true})
	if err != nil {
		t.Fatal(err)
	}
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
	if err := request.Options.Extensions.Set("test/openai_request", map[string]any{"temperature": 0.7}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "not supported by this provider") {
		t.Fatalf("Call=%v", err)
	}
}
