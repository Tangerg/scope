package elevenlabs

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-resty/resty/v2"
)

const maximumErrorResponseBytes = int64(64 * 1024)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("elevenlabs: APIKey is required")
	}
	return nil
}

type api struct {
	http *resty.Client
}

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	client := resty.New()
	if config.HTTPClient != nil {
		client = resty.NewWithClient(config.HTTPClient)
	}
	client.SetBaseURL(cmp.Or(config.BaseURL, DefaultBaseURL)).
		SetHeader("xi-api-key", config.APIKey).
		SetHeader("Accept", "audio/*")

	return &api{http: client}, nil
}

type ttsRequest struct {
	EnableLogging                   *bool                            `json:"-"`
	OptimizeStreamingLatency        *int                             `json:"-"`
	Text                            string                           `json:"text"`
	ModelID                         string                           `json:"model_id,omitempty"`
	LanguageCode                    string                           `json:"language_code,omitempty"`
	VoiceSettings                   *voiceSettings                   `json:"voice_settings,omitempty"`
	Seed                            *int64                           `json:"seed,omitempty"`
	PreviousText                    string                           `json:"previous_text,omitempty"`
	NextText                        string                           `json:"next_text,omitempty"`
	PreviousRequestIDs              []string                         `json:"previous_request_ids,omitzero"`
	NextRequestIDs                  []string                         `json:"next_request_ids,omitzero"`
	ApplyTextNormalization          string                           `json:"apply_text_normalization,omitempty"`
	ApplyLanguageTextNormalization  *bool                            `json:"apply_language_text_normalization,omitempty"`
	PronunciationDictionaryLocators []pronunciationDictionaryLocator `json:"pronunciation_dictionary_locators,omitzero"`
}

type pronunciationDictionaryLocator struct {
	PronunciationDictionaryID string `json:"pronunciation_dictionary_id"`
	VersionID                 string `json:"version_id,omitempty"`
}

type voiceSettings struct {
	Stability       *float64 `json:"stability,omitempty"`
	SimilarityBoost *float64 `json:"similarity_boost,omitempty"`
	Style           *float64 `json:"style,omitempty"`
	UseSpeakerBoost *bool    `json:"use_speaker_boost,omitempty"`
	Speed           *float64 `json:"speed,omitempty"`
}

// textToSpeech buffers the entire audio body into memory and returns it
// alongside the response headers (used by callers to surface mime type
// and request id).
func (a *api) textToSpeech(ctx context.Context, voiceID, outputFormat string, body *ttsRequest) ([]byte, http.Header, error) {
	resp, err := a.buildAudioRequest(ctx, outputFormat, body).Post("/text-to-speech/" + voiceID)
	if err != nil {
		return nil, nil, fmt.Errorf("elevenlabs: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, nil, fmt.Errorf("elevenlabs: http %d: %s", resp.StatusCode(), resp.String())
	}
	return resp.Body(), resp.Header(), nil
}

// textToSpeechStream opts out of resty's response parsing so callers can
// stream audio chunks directly off the wire. The returned ReadCloser
// MUST be closed by the caller.
func (a *api) textToSpeechStream(ctx context.Context, voiceID, outputFormat string, body *ttsRequest) (io.ReadCloser, http.Header, error) {
	req := a.buildAudioRequest(ctx, outputFormat, body).SetDoNotParseResponse(true)
	resp, err := req.Post("/text-to-speech/" + voiceID + "/stream")
	if err != nil {
		return nil, nil, fmt.Errorf("elevenlabs: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		raw := resp.RawBody()
		defer raw.Close()
		errBody, readErr := io.ReadAll(io.LimitReader(raw, maximumErrorResponseBytes))
		if readErr != nil {
			return nil, nil, fmt.Errorf("elevenlabs: http %d; read error response: %w", resp.StatusCode(), readErr)
		}
		return nil, nil, fmt.Errorf("elevenlabs: http %d: %s", resp.StatusCode(), string(errBody))
	}
	return resp.RawBody(), resp.Header(), nil
}

func (a *api) buildAudioRequest(ctx context.Context, outputFormat string, body *ttsRequest) *resty.Request {
	req := a.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(body)
	if outputFormat != "" {
		req = req.SetQueryParam("output_format", outputFormat)
	}
	if body.EnableLogging != nil {
		req.SetQueryParam("enable_logging", strconv.FormatBool(*body.EnableLogging))
	}
	if body.OptimizeStreamingLatency != nil {
		req.SetQueryParam("optimize_streaming_latency", strconv.Itoa(*body.OptimizeStreamingLatency))
	}
	return req
}

// TranscriptionRequest mirrors POST /v1/speech-to-text (multipart/form-data).
// Audio is uploaded as the "file" form field; other parameters are form fields.
// It intentionally models only synchronous, single-result options representable
// by Core's transcription protocol.
type transcriptionRequest struct {
	ModelID               string
	LanguageCode          string
	Diarize               *bool
	NumSpeakers           *int
	TagAudioEvents        *bool
	TimestampsGranularity string
	DiarizationThreshold  *float64
	FileFormat            string
	Temperature           *float64
	Seed                  *int
	NoVerbatim            *bool
	UseSpeakerLibrary     *bool
	DetectSpeakerRoles    *bool
	Keyterms              []string
}

func (t *transcriptionRequest) form() (map[string]string, error) {
	form := map[string]string{}
	if t == nil {
		return form, nil
	}
	if t.ModelID != "" {
		form["model_id"] = t.ModelID
	}
	if t.LanguageCode != "" {
		form["language_code"] = t.LanguageCode
	}
	if t.Diarize != nil {
		form["diarize"] = strconv.FormatBool(*t.Diarize)
	}
	if t.NumSpeakers != nil {
		form["num_speakers"] = strconv.Itoa(*t.NumSpeakers)
	}
	if t.TagAudioEvents != nil {
		form["tag_audio_events"] = strconv.FormatBool(*t.TagAudioEvents)
	}
	if t.TimestampsGranularity != "" {
		form["timestamps_granularity"] = t.TimestampsGranularity
	}
	if t.DiarizationThreshold != nil {
		form["diarization_threshold"] = strconv.FormatFloat(*t.DiarizationThreshold, 'f', -1, 64)
	}
	if t.FileFormat != "" {
		form["file_format"] = t.FileFormat
	}
	if t.Temperature != nil {
		form["temperature"] = strconv.FormatFloat(*t.Temperature, 'f', -1, 64)
	}
	if t.Seed != nil {
		form["seed"] = strconv.Itoa(*t.Seed)
	}
	if t.NoVerbatim != nil {
		form["no_verbatim"] = strconv.FormatBool(*t.NoVerbatim)
	}
	if t.UseSpeakerLibrary != nil {
		form["use_speaker_library"] = strconv.FormatBool(*t.UseSpeakerLibrary)
	}
	if t.DetectSpeakerRoles != nil {
		form["detect_speaker_roles"] = strconv.FormatBool(*t.DetectSpeakerRoles)
	}
	if len(t.Keyterms) > 0 {
		encoded, err := json.Marshal(t.Keyterms)
		if err != nil {
			return nil, fmt.Errorf("elevenlabs: encode transcription keyterms: %w", err)
		}
		form["keyterms"] = string(encoded)
	}
	return form, nil
}

func (t *transcriptionRequest) validate() error {
	if t.ModelID != ModelScribeV2 && t.ModelID != ModelScribeV1 {
		return fmt.Errorf("elevenlabs: transcription model must be %q or %q, got %q", ModelScribeV2, ModelScribeV1, t.ModelID)
	}
	if t.NumSpeakers != nil && (*t.NumSpeakers < 1 || *t.NumSpeakers > 32) {
		return fmt.Errorf("elevenlabs: num_speakers must be between 1 and 32, got %d", *t.NumSpeakers)
	}
	if t.DiarizationThreshold != nil && (*t.DiarizationThreshold < 0.1 || *t.DiarizationThreshold > 0.4) {
		return fmt.Errorf("elevenlabs: diarization_threshold must be between 0.1 and 0.4, got %g", *t.DiarizationThreshold)
	}
	if t.Temperature != nil && (*t.Temperature < 0 || *t.Temperature > 2) {
		return fmt.Errorf("elevenlabs: transcription temperature must be between 0 and 2, got %g", *t.Temperature)
	}
	if t.Seed != nil && *t.Seed < 0 {
		return fmt.Errorf("elevenlabs: transcription seed must be non-negative, got %d", *t.Seed)
	}
	if t.TimestampsGranularity != "" && t.TimestampsGranularity != "none" && t.TimestampsGranularity != "word" && t.TimestampsGranularity != "character" {
		return fmt.Errorf("elevenlabs: timestamps_granularity must be none, word, or character, got %q", t.TimestampsGranularity)
	}
	if t.FileFormat != "" && t.FileFormat != "other" && t.FileFormat != "pcm_s16le_16" {
		return fmt.Errorf("elevenlabs: file_format must be other or pcm_s16le_16, got %q", t.FileFormat)
	}
	if len(t.Keyterms) > maximumKeyterms {
		return fmt.Errorf("elevenlabs: keyterms must contain at most %d entries, got %d", maximumKeyterms, len(t.Keyterms))
	}
	if t.ModelID == ModelScribeV1 && len(t.Keyterms) > 0 {
		return errors.New("elevenlabs: keyterms are only supported by scribe_v2")
	}
	return nil
}

// TranscriptionResponse models /v1/speech-to-text JSON output.
type transcriptionResponse struct {
	LanguageCode        string                `json:"language_code"`
	LanguageProbability float64               `json:"language_probability"`
	Text                string                `json:"text"`
	Words               []transcriptionWord   `json:"words"`
	Entities            []transcriptionEntity `json:"entities,omitempty"`
}

type transcriptionWord struct {
	Text         string  `json:"text"`
	Type         string  `json:"type"`
	Start        float64 `json:"start"`
	End          float64 `json:"end"`
	SpeakerID    string  `json:"speaker_id,omitempty"`
	ChannelIndex *int    `json:"channel_index,omitempty"`
}

type transcriptionEntity struct {
	Text       string `json:"text"`
	EntityType string `json:"entity_type"`
	StartChar  int    `json:"start_char"`
	EndChar    int    `json:"end_char"`
}

func (a *api) transcription(ctx context.Context, audio []byte, mimeType string, req *transcriptionRequest) (*transcriptionResponse, error) {
	if len(audio) == 0 {
		return nil, errors.New("elevenlabs: transcription audio must not be empty")
	}
	form, err := req.form()
	if err != nil {
		return nil, err
	}

	var out transcriptionResponse
	r := a.http.R().
		SetContext(ctx).
		SetMultipartField("file", "audio", cmp.Or(mimeType, "application/octet-stream"), bytes.NewReader(audio)).
		SetMultipartFormData(form).
		SetResult(&out)
	resp, err := r.Post("/speech-to-text")
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("elevenlabs: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}
