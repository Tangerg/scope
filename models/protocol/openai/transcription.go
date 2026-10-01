package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/Tangerg/scope/core/transcription"
)

// TranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type TranscriptionModelConfig struct {
	Provider       string
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (t TranscriptionModelConfig) Validate() error {
	if err := validateProvider(t.Provider); err != nil {
		return fmt.Errorf("openai: Provider: %w", err)
	}
	if err := (apiConfig{APIKey: t.APIKey, BaseURL: t.BaseURL}).validate(); err != nil {
		return err
	}
	if t.DefaultOptions.Model == "" {
		return errors.New("openai: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ transcription.Model = (*TranscriptionModel)(nil)

// TranscriptionModel implements the OpenAI-compatible transcription
// protocol.
type TranscriptionModel struct {
	api            *api
	provider       string
	defaultOptions transcription.Options
}

// NewTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewTranscriptionModel(_ context.Context, config TranscriptionModelConfig) (*TranscriptionModel, error) {
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

	return &TranscriptionModel{
		api:            api,
		provider:       config.Provider,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (t *TranscriptionModel) buildAPITranscriptionRequest(req *transcription.Request) (*openai.AudioTranscriptionNewParams, error) {
	effectiveOptions, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	fields, err := decodeRequestFields(effectiveOptions.Extensions, protocolModalityRequestExtensionKey(t.provider, "transcription"), "model", "file", "language")
	if err != nil {
		return nil, err
	}
	params := &openai.AudioTranscriptionNewParams{}
	params.SetExtraFields(fields)

	params.Model = effectiveOptions.Model
	if effectiveOptions.Language != "" {
		params.Language = param.NewOpt(effectiveOptions.Language)
	}

	params.File, err = audioFile(req.Audio)
	if err != nil {
		return nil, err
	}

	return params, nil
}

func (t *TranscriptionModel) buildTranscriptionResponse(resp *openai.AudioTranscriptionNewResponseUnion) (*transcription.Response, error) {
	output, err := transcription.NewOutput(resp.Text, nil)
	if err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, &transcription.ResponseMetadata{})
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	apiReq, err := t.buildAPITranscriptionRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := t.api.transcription(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	return t.buildTranscriptionResponse(apiResp)
}
