package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/Tangerg/scope/core/transcription"
)

// AudioTranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type AudioTranscriptionModelConfig struct {
	Provider       string
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (a AudioTranscriptionModelConfig) Validate() error {
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

var _ transcription.Model = (*AudioTranscriptionModel)(nil)

// AudioTranscriptionModel implements the OpenAI-compatible transcription
// protocol.
type AudioTranscriptionModel struct {
	api            *api
	provider       string
	defaultOptions transcription.Options
}

// NewAudioTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewAudioTranscriptionModel(_ context.Context, config AudioTranscriptionModelConfig) (*AudioTranscriptionModel, error) {
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

	return &AudioTranscriptionModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (a *AudioTranscriptionModel) buildAPITranscriptionRequest(req *transcription.Request) (*openai.AudioTranscriptionNewParams, error) {
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	fields, err := decodeRequestFields(effectiveOptions.Extensions, protocolModalityRequestExtensionKey(a.provider, "transcription"), "model", "file", "language")
	if err != nil {
		return nil, err
	}
	params := &openai.AudioTranscriptionNewParams{}
	params.SetExtraFields(fields)

	params.Model = effectiveOptions.Model
	if effectiveOptions.Language != "" {
		params.Language = param.NewOpt(effectiveOptions.Language)
	}

	data, err := req.Audio.Bytes()
	if err != nil {
		return nil, err
	}

	params.File = bytes.NewReader(data)

	return params, nil
}

func (a *AudioTranscriptionModel) buildTranscriptionResponse(resp *openai.AudioTranscriptionNewResponseUnion) (*transcription.Response, error) {
	output, err := transcription.NewOutput(resp.Text, nil)
	if err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, &transcription.ResponseMetadata{})
}

func (a *AudioTranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	apiReq, err := a.buildAPITranscriptionRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := a.api.audioTranscription(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	return a.buildTranscriptionResponse(apiResp)
}
