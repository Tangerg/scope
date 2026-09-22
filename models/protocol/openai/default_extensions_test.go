package openai

import (
	jsonv2 "encoding/json/v2"
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
