package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/transcription"
)

type TranscriptionModelConfig struct {
	Provider       string
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (t TranscriptionModelConfig) Validate() error {
	if err := validateBinding(t.Provider, t.APIKey, t.BaseURL); err != nil {
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

type TranscriptionModel struct {
	api            *api
	provider       string
	defaultOptions transcription.Options
}

func NewTranscriptionModel(ctx context.Context, config TranscriptionModelConfig) (*TranscriptionModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, apiConfig{
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

	params, err := decodeAudioRequest[openai.AudioTranscriptionNewParams](effectiveOptions.Extensions, t.provider+TranscriptionRequestExtensionSuffix, audioFieldModel, audioFieldFile, audioFieldLanguage)
	if err != nil {
		return nil, err
	}

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
	if resp == nil {
		return nil, fmt.Errorf("%w: openai: transcription requires text", transcription.ErrInvalidResponse)
	}
	text, err := transcriptionWireText(resp.RawJSON())
	if err != nil {
		return nil, fmt.Errorf("%w: openai: transcription: %w", transcription.ErrInvalidResponse, err)
	}
	output, err := transcription.NewOutput(text, nil)
	if err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, &transcription.ResponseMetadata{})
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	apiReq, err := t.buildAPITranscriptionRequest(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}

	apiResp, err := t.api.transcription(ctx, apiReq)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}

	return t.buildTranscriptionResponse(apiResp)
}
