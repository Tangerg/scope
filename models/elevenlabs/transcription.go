package elevenlabs

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
)

const (
	transcriptionFieldModelID      = "model_id"
	transcriptionFieldLanguageCode = "language_code"
	metadataLanguageCode           = "elevenlabs/language_code"
	metadataLanguageProbability    = "elevenlabs/language_probability"
	metadataWords                  = "elevenlabs/words"
	metadataEntities               = "elevenlabs/entities"
)

type TranscriptionModelConfig struct {
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (t TranscriptionModelConfig) apiConfig() apiConfig {
	return apiConfig{
		APIKey:     t.APIKey,
		BaseURL:    t.BaseURL,
		HTTPClient: t.HTTPClient,
	}
}

func (t TranscriptionModelConfig) Validate() error {
	if err := t.apiConfig().validate(); err != nil {
		return err
	}
	if t.DefaultOptions.Model == "" {
		return errors.New("elevenlabs: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ transcription.Model = (*TranscriptionModel)(nil)

// TranscriptionModel wraps ElevenLabs' /v1/speech-to-text endpoint
// (Scribe model family). Language uses transcription.Options.Language.
// Diarization and per-word timestamps use Options.Extensions under
// TranscriptionRequestExtensionKey.
type TranscriptionModel struct {
	api            *api
	defaultOptions transcription.Options
}

func NewTranscriptionModel(ctx context.Context, config TranscriptionModelConfig) (*TranscriptionModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, config.apiConfig())
	if err != nil {
		return nil, err
	}

	return &TranscriptionModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

// nativeTranscriptionRequest reads the native options extension. Core owns the
// model and language, so the extension may not name either, even as null.
func nativeTranscriptionRequest(options transcription.Options) (*transcriptionRequest, error) {
	members, _, err := options.Extensions.Decode[map[string]jsontext.Value](TranscriptionRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{transcriptionFieldModelID, transcriptionFieldLanguageCode} {
		if _, named := members[field]; named {
			return nil, fmt.Errorf("elevenlabs: extension %q field %q: %w", TranscriptionRequestExtensionKey, field, metadata.ErrCoreOwnedField)
		}
	}
	native, _, err := options.Extensions.Decode[transcriptionRequest](TranscriptionRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	native.ModelID, native.LanguageCode = options.Model, options.Language
	if err := native.validate(); err != nil {
		return nil, err
	}
	return &native, nil
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	effectiveOptions, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	apiReq, err := nativeTranscriptionRequest(effectiveOptions)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}

	audio, err := req.Audio.Bytes()
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}

	apiResp, err := t.api.transcription(ctx, audio, req.Audio.MIME, apiReq)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}

	outputMetadata, err := apiResp.outputMetadata()
	if err != nil {
		return nil, err
	}

	if apiResp.Text == nil {
		return nil, fmt.Errorf("%w: elevenlabs: transcription requires text", transcription.ErrInvalidResponse)
	}
	output, err := transcription.NewOutput(*apiResp.Text, outputMetadata)
	if err != nil {
		return nil, err
	}

	responseMetadata := &transcription.ResponseMetadata{Model: apiReq.ModelID}
	if err := responseMetadata.Extra.Set(TranscriptionResponseExtensionKey, apiResp.Raw); err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, responseMetadata)
}
