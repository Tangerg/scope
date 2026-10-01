package elevenlabs

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
)

const maximumKeyterms = 1000

// TranscriptionModelConfig binds provider access and defaults shared by every transcription call.
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
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	effectiveOptions, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	nativeFields, _, decodeErr := effectiveOptions.Extensions.Decode[map[string]any](TranscriptionRequestExtensionKey)
	if decodeErr != nil {
		return nil, decodeErr
	}
	for _, field := range []string{"model_id", "language_code"} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("elevenlabs: extension %q field %q is owned by Core", TranscriptionRequestExtensionKey, field)
		}
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[transcriptionRequest](TranscriptionRequestExtensionKey)
	apiReq := &apiReqValue
	if err != nil {
		return nil, err
	}
	apiReq.ModelID = effectiveOptions.Model
	apiReq.LanguageCode = effectiveOptions.Language
	if validateTranscriptionRequestErr := apiReq.validate(); validateTranscriptionRequestErr != nil {
		return nil, validateTranscriptionRequestErr
	}

	audio, err := req.Audio.Bytes()
	if err != nil {
		return nil, err
	}

	apiResp, err := t.api.transcription(ctx, audio, req.Audio.MIME, apiReq)
	if err != nil {
		return nil, err
	}

	var outputMetadata metadata.Map
	if apiResp.LanguageCode != "" {
		if setErr := outputMetadata.Set("elevenlabs/language_code", apiResp.LanguageCode); setErr != nil {
			return nil, setErr
		}
		if setErr := outputMetadata.Set("elevenlabs/language_probability", apiResp.LanguageProbability); setErr != nil {
			return nil, setErr
		}
	}
	if len(apiResp.Words) > 0 {
		if setErr := outputMetadata.Set("elevenlabs/words", apiResp.Words); setErr != nil {
			return nil, setErr
		}
	}
	if len(apiResp.Entities) > 0 {
		if setErr := outputMetadata.Set("elevenlabs/entities", apiResp.Entities); setErr != nil {
			return nil, setErr
		}
	}

	output, err := transcription.NewOutput(apiResp.Text, outputMetadata)
	if err != nil {
		return nil, err
	}

	responseMetadata := &transcription.ResponseMetadata{Model: apiReq.ModelID}
	if err := responseMetadata.Extra.Set(TranscriptionResponseExtensionKey, apiResp); err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, responseMetadata)
}
