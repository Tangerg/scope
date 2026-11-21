package openai

import (
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/transcription"
)

// Defaults are the options of every request that overrides nothing, so a
// default the endpoint refuses must fail the binding, not every later call.
func TestConstructionRefusesDefaultsNoRequestCouldSend(t *testing.T) {
	limit := 1.0
	dialect := DialectConfig{
		Provider:         "compatible",
		TokenLimitField:  TokenLimitMaxTokens,
		IgnoredOptions:   []ChatOption{ChatOptionPresencePenalty},
		MaxTemperature:   &limit,
		ReasoningEfforts: []corechat.ReasoningEffort{"default"},
	}
	for name, defaults := range map[string]corechat.Options{
		"ignored option":   {PresencePenalty: new(0.5)},
		"top_k":            {TopK: new(int64(4))},
		"temperature":      {Temperature: new(1.5)},
		"reasoning effort": {ReasoningEffort: "high"},
	} {
		config := ChatModelConfig{APIKey: "test", BaseURL: "https://compatible.example/v1", DefaultOptions: defaults}
		if _, err := NewChatCompletions(t.Context(), config, dialect); err == nil {
			t.Errorf("NewChatCompletions accepted %s defaults", name)
		}
	}

	for name, defaults := range map[string]corechat.Options{
		"stop":             {Stop: []string{"END"}},
		"reasoning effort": {ReasoningEffort: "default"},
	} {
		config := ChatModelConfig{APIKey: "test", DefaultOptions: defaults}
		if _, err := NewResponses(t.Context(), config, ResponsesDialect{Provider: Provider}); err == nil {
			t.Errorf("NewResponses accepted %s defaults", name)
		}
	}

	imageConfig := ImageModelConfig{Provider: Provider, APIKey: "test", DefaultOptions: image.Options{Model: "gpt-image-2", Seed: new(int64(7))}}
	if _, err := NewImageModel(t.Context(), imageConfig); err == nil {
		t.Error("NewImageModel accepted an unsupported default seed")
	}
	translationConfig := AudioTranslationModelConfig{Provider: Provider, APIKey: "test", DefaultOptions: transcription.Options{Model: "whisper-1", Language: "fr"}}
	if _, err := NewAudioTranslationModel(t.Context(), translationConfig); err == nil {
		t.Error("NewAudioTranslationModel accepted an unsupported default language")
	}
}
