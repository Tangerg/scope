package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/transcription"
)

// AudioTranslationModelConfig configures the OpenAI /audio/translations
// backend. Only "whisper-1" is currently accepted by OpenAI — newer
// gpt-4o-transcribe models are transcription-only and reject translation
// calls.
type AudioTranslationModelConfig struct {
	Provider       string
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (a AudioTranslationModelConfig) Validate() error {
	if err := validateProvider(a.Provider); err != nil {
		return fmt.Errorf("openai: Provider: %w", err)
	}
	if a.APIKey == "" {
		return errors.New("openai: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("openai: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ transcription.Model = (*AudioTranslationModel)(nil)

// AudioTranslationModel exposes OpenAI's /audio/translations endpoint —
// it accepts audio in any supported language and returns the **English**
// translation. The wire shape is the same as transcription (audio in,
// text out), so it implements the [transcription.Model] interface and
// can drop into any code path that already uses transcription.
//
// If the caller needs the original-language transcript instead of a
// translation, use [AudioTranscriptionModel].
type AudioTranslationModel struct {
	api            *api
	provider       string
	defaultOptions transcription.Options
}

// NewAudioTranslationModel rejects an invalid provider binding before the first translation call.
func NewAudioTranslationModel(_ context.Context, config AudioTranslationModelConfig) (*AudioTranslationModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}

	return &AudioTranslationModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (a *AudioTranslationModel) buildAPITranslationRequest(req *transcription.Request) (*openai.AudioTranslationNewParams, error) {
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	if rejectUnsupportedOptionsErr := rejectUnsupportedOptions("openai: translation", map[string]bool{
		"language": effectiveOptions.Language != "",
	}); rejectUnsupportedOptionsErr != nil {
		return nil, rejectUnsupportedOptionsErr
	}

	fields, err := decodeRequestFields(effectiveOptions.Extensions, protocolModalityRequestExtensionKey(a.provider, "translation"), "model", "file")
	if err != nil {
		return nil, err
	}
	params := &openai.AudioTranslationNewParams{}
	params.SetExtraFields(fields)

	params.Model = effectiveOptions.Model
	data, err := req.Audio.Bytes()
	if err != nil {
		return nil, err
	}
	params.File = bytes.NewReader(data)

	return params, nil
}

func (a *AudioTranslationModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	apiReq, err := a.buildAPITranslationRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := a.api.audioTranslation(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	output, err := transcription.NewOutput(apiResp.Text, nil)
	if err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, &transcription.ResponseMetadata{})
}
