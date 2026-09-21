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

// AudioTranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type AudioTranscriptionModelConfig struct {
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (a AudioTranscriptionModelConfig) Validate() error {
	if a.APIKey == "" {
		return errors.New("elevenlabs: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("elevenlabs: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ transcription.Model = (*AudioTranscriptionModel)(nil)

// AudioTranscriptionModel wraps ElevenLabs' /v1/speech-to-text endpoint
// (Scribe model family). Diarization / language / per-word timestamps
// are reached through the extension-threaded [TranscriptionRequest].
type AudioTranscriptionModel struct {
	api            *api
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
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (a *AudioTranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
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

	apiResp, err := a.api.transcription(ctx, audio, req.Audio.MIME, apiReq)
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
