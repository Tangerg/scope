package elevenlabs

import (
	"context"
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

func (t TranscriptionModelConfig) Validate() error {
	if t.APIKey == "" {
		return errors.New("elevenlabs: APIKey is required")
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
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	effectiveOptions, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	nativeFields, _, decodeErr := effectiveOptions.Extensions.Decode[map[string]any](TranscriptionRequestExtensionKey)
	if decodeErr != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: decodeErr}
	}
	for _, field := range []string{transcriptionFieldModelID, transcriptionFieldLanguageCode} {
		if _, exists := nativeFields[field]; exists {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: fmt.Errorf("elevenlabs: extension %q field %q is owned by Core", TranscriptionRequestExtensionKey, field)}
		}
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[transcriptionRequest](TranscriptionRequestExtensionKey)
	apiReq := &apiReqValue
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	apiReq.ModelID = effectiveOptions.Model
	apiReq.LanguageCode = effectiveOptions.Language
	if validateTranscriptionRequestErr := apiReq.validate(); validateTranscriptionRequestErr != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: validateTranscriptionRequestErr}
	}

	audio, err := req.Audio.Bytes()
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}

	apiResp, err := t.api.transcription(ctx, audio, req.Audio.MIME, apiReq)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}

	var outputMetadata metadata.Map
	if apiResp.LanguageCode != "" {
		if setErr := outputMetadata.Set(metadataLanguageCode, apiResp.LanguageCode); setErr != nil {
			return nil, setErr
		}
	}
	if apiResp.LanguageProbability != nil {
		if setErr := outputMetadata.Set(metadataLanguageProbability, *apiResp.LanguageProbability); setErr != nil {
			return nil, setErr
		}
	}
	if len(apiResp.Words) > 0 {
		if setErr := outputMetadata.Set(metadataWords, apiResp.Words); setErr != nil {
			return nil, setErr
		}
	}
	if len(apiResp.Entities) > 0 {
		if setErr := outputMetadata.Set(metadataEntities, apiResp.Entities); setErr != nil {
			return nil, setErr
		}
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
