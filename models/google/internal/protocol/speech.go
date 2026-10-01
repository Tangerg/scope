package protocol

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/speech"
)

// SpeechModelConfig binds provider access and defaults shared by every speech call.
type SpeechModelConfig struct {
	Provider       string
	Client         ClientConfig
	DefaultOptions speech.Options
}

func (s SpeechModelConfig) Validate() error {
	if err := validateProvider(s.Provider); err != nil {
		return fmt.Errorf("google: Provider: %w", err)
	}
	if err := s.Client.Validate(); err != nil {
		return err
	}
	if s.DefaultOptions.Model == "" {
		return errors.New("google: DefaultOptions.Model is required")
	}
	if err := s.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ speech.Model = (*SpeechModel)(nil)

// SpeechModel owns Gemini 2.5's unary-only synthesis protocol.
// Gemini 3.1 uses StreamingSpeechModel for both aggregate and streaming output.
//
// Speed and OutputFormat are not honored: Gemini's TTS has no
// playback-rate knob. GenerateContent returns 24 kHz signed 16-bit
// little-endian PCM; callers choose their own container at the application
// boundary.
type SpeechModel struct{ binding *speechBinding }

func NewSpeechModel(ctx context.Context, config SpeechModelConfig) (*SpeechModel, error) {
	if err := validateUnarySpeechModel(config.DefaultOptions.Model); err != nil {
		return nil, err
	}
	binding, err := newSpeechBinding(ctx, config)
	if err != nil {
		return nil, err
	}
	return &SpeechModel{binding: binding}, nil
}

func (s *SpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	modelName, contents, config, err := s.binding.buildAPITTSRequest(req)
	if err != nil {
		return nil, err
	}
	if modelErr := validateUnarySpeechModel(modelName); modelErr != nil {
		return nil, modelErr
	}

	apiResp, err := s.binding.api.chatCompletion(ctx, modelName, contents, config)
	if err != nil {
		return nil, err
	}
	if err := validateProtocolCompletion(apiResp); err != nil {
		return nil, fmt.Errorf("google: speech: %w: %w", speech.ErrInvalidResponse, err)
	}
	return s.binding.buildTTSResponse(apiResp)
}

func validateUnarySpeechModel(model string) error {
	if model != ModelGemini25FlashPreviewTTS && model != ModelGemini25ProPreviewTTS {
		return fmt.Errorf("google: unary speech requires %q or %q; Gemini 3.1 uses StreamingSpeechModel", ModelGemini25FlashPreviewTTS, ModelGemini25ProPreviewTTS)
	}
	return nil
}
