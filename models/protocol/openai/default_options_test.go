package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
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

// A binding may leave the model to each request; Core requires it of the
// resolved options, so a request that names none fails before any I/O.
func TestModelMayComeFromEachRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,2]}],"model":"m"}`))
	}))
	t.Cleanup(server.Close)
	model, err := NewEmbeddingModel(t.Context(), EmbeddingModelConfig{Provider: Provider, APIKey: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"text"}}); !errors.Is(err, embedding.ErrInvalidOptions) || calls.Load() != 0 {
		t.Fatalf("Call without a model = %v, requests = %d", err, calls.Load())
	}
	request := &embedding.Request{Texts: []string{"text"}, Options: embedding.Options{Model: "text-embedding-3-small"}}
	if _, err := model.Call(t.Context(), request); err != nil || calls.Load() != 1 {
		t.Fatalf("Call with a request model = %v, requests = %d", err, calls.Load())
	}
}
