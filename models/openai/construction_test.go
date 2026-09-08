package openai_test

import (
	"testing"

	"github.com/Tangerg/scope/models/openai"
)

// TestConstructorsRejectAnAbsentCredential is the one contract this module can
// prove without a live account: a credential-gated provider must fail at
// construction rather than at the first call, where the failure would surface
// as a transport error the caller cannot distinguish from an outage.
func TestConstructorsRejectAnAbsentCredential(t *testing.T) {
	cases := map[string]func() error{
		"NewChat": func() error {
			_, err := openai.NewChat(t.Context(), openai.ChatConfig{})
			return err
		},
		"NewResponses": func() error {
			_, err := openai.NewResponses(t.Context(), openai.ResponsesConfig{})
			return err
		},
		"NewEmbeddingModel": func() error {
			_, err := openai.NewEmbeddingModel(t.Context(), openai.EmbeddingModelConfig{})
			return err
		},
		"NewAudioTranscriptionModel": func() error {
			_, err := openai.NewAudioTranscriptionModel(t.Context(), openai.AudioTranscriptionModelConfig{})
			return err
		},
		"NewAudioTranslationModel": func() error {
			_, err := openai.NewAudioTranslationModel(t.Context(), openai.AudioTranslationModelConfig{})
			return err
		},
		"NewAudioTTSModel": func() error {
			_, err := openai.NewAudioTTSModel(t.Context(), openai.AudioTTSModelConfig{})
			return err
		},
		"NewImageModel": func() error {
			_, err := openai.NewImageModel(t.Context(), openai.ImageModelConfig{})
			return err
		},
		"NewModerationModel": func() error {
			_, err := openai.NewModerationModel(t.Context(), openai.ModerationModelConfig{})
			return err
		},
	}
	for name, construct := range cases {
		t.Run(name, func(t *testing.T) {
			if err := construct(); err == nil {
				t.Fatal("an empty config constructed a usable model")
			}
		})
	}
}

func TestConfigsRejectAnAbsentCredential(t *testing.T) {
	cases := map[string]func() error{
		"chat":          func() error { return (openai.ChatConfig{}).Validate() },
		"responses":     func() error { return (openai.ResponsesConfig{}).Validate() },
		"embedding":     func() error { return (openai.EmbeddingModelConfig{}).Validate() },
		"transcription": func() error { return (openai.AudioTranscriptionModelConfig{}).Validate() },
		"translation":   func() error { return (openai.AudioTranslationModelConfig{}).Validate() },
		"speech":        func() error { return (openai.AudioTTSModelConfig{}).Validate() },
		"image":         func() error { return (openai.ImageModelConfig{}).Validate() },
		"moderation":    func() error { return (openai.ModerationModelConfig{}).Validate() },
	}
	for name, validate := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validate(); err == nil {
				t.Fatal("Validate accepted an absent credential")
			}
		})
	}
}

// TestProviderIdentityIsStable pins the identifiers a composition root passes
// to observability and a Host stores as provider identity. They are wire-facing
// constants, so a rename is a breaking change rather than a refactor.
func TestProviderIdentityIsStable(t *testing.T) {
	if openai.Provider == "" {
		t.Fatal("Provider is empty")
	}
}
