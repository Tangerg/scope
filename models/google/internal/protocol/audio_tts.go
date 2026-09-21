package protocol

import (
	"context"
	"errors"
	"fmt"

	tts "github.com/Tangerg/scope/core/speech"
)

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	Provider       string
	Client         ClientConfig
	DefaultOptions tts.Options
}

func (a AudioTTSModelConfig) Validate() error {
	if err := validateProvider(a.Provider); err != nil {
		return fmt.Errorf("google: Provider: %w", err)
	}
	if err := a.Client.Validate(); err != nil {
		return err
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("google: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ tts.Model = (*AudioTTSModel)(nil)

// AudioTTSModel wraps Gemini's native TTS through GenerateContent with
// ResponseModalities=AUDIO. Current supported models are declared in
// constant.go. Incremental synthesis uses StreamingAudioTTSModel.
//
// Speed and OutputFormat are not honored: Gemini's TTS has no
// playback-rate knob. GenerateContent returns 24 kHz signed 16-bit
// little-endian PCM; callers choose their own container at the application
// boundary.
type AudioTTSModel struct{ binding *speechBinding }

func NewAudioTTSModel(ctx context.Context, config AudioTTSModelConfig) (*AudioTTSModel, error) {
	binding, err := newSpeechBinding(ctx, config)
	if err != nil {
		return nil, err
	}
	return &AudioTTSModel{binding: binding}, nil
}

func (a *AudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	modelName, contents, config, err := a.binding.buildAPITTSRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := a.binding.api.chatCompletion(ctx, modelName, contents, config)
	if err != nil {
		return nil, err
	}

	return a.binding.buildTTSResponse(apiResp)
}
