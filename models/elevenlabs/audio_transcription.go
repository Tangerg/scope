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
	apiReqValue, _, err := effectiveOptions.Extensions.Decode[transcriptionRequest](TranscriptionRequestExtensionKey)
	apiReq := &apiReqValue
	if err != nil {
		return nil, err
	}
	if apiReq.ModelID == "" {
		apiReq.ModelID = effectiveOptions.Model
	}
	if apiReq.LanguageCode == "" && effectiveOptions.Language != "" {
		apiReq.LanguageCode = effectiveOptions.Language
	}
	if validateTranscriptionRequestErr := validateTranscriptionRequest(apiReq); validateTranscriptionRequestErr != nil {
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

func validateTranscriptionRequest(req *transcriptionRequest) error {
	if req.ModelID != ModelScribeV2 && req.ModelID != ModelScribeV1 {
		return fmt.Errorf("elevenlabs: transcription model must be %q or %q, got %q", ModelScribeV2, ModelScribeV1, req.ModelID)
	}
	if req.NumSpeakers != nil && (*req.NumSpeakers < 1 || *req.NumSpeakers > 32) {
		return fmt.Errorf("elevenlabs: num_speakers must be between 1 and 32, got %d", *req.NumSpeakers)
	}
	if req.DiarizationThreshold != nil && (*req.DiarizationThreshold < 0.1 || *req.DiarizationThreshold > 0.4) {
		return fmt.Errorf("elevenlabs: diarization_threshold must be between 0.1 and 0.4, got %g", *req.DiarizationThreshold)
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return fmt.Errorf("elevenlabs: transcription temperature must be between 0 and 2, got %g", *req.Temperature)
	}
	if req.Seed != nil && *req.Seed < 0 {
		return fmt.Errorf("elevenlabs: transcription seed must be non-negative, got %d", *req.Seed)
	}
	if req.TimestampsGranularity != "" && req.TimestampsGranularity != "none" && req.TimestampsGranularity != "word" && req.TimestampsGranularity != "character" {
		return fmt.Errorf("elevenlabs: timestamps_granularity must be none, word, or character, got %q", req.TimestampsGranularity)
	}
	if req.FileFormat != "" && req.FileFormat != "other" && req.FileFormat != "pcm_s16le_16" {
		return fmt.Errorf("elevenlabs: file_format must be other or pcm_s16le_16, got %q", req.FileFormat)
	}
	if len(req.Keyterms) > maximumKeyterms {
		return fmt.Errorf("elevenlabs: keyterms must contain at most %d entries, got %d", maximumKeyterms, len(req.Keyterms))
	}
	if req.ModelID == ModelScribeV1 && len(req.Keyterms) > 0 {
		return errors.New("elevenlabs: keyterms are only supported by scribe_v2")
	}
	return nil
}
