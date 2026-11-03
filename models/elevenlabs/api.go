package elevenlabs

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/core/httpclient"
)

const (
	minimumTranscriptionSpeakers        = 1
	maximumTranscriptionSpeakers        = 32
	minimumDiarizationThreshold         = 0.1
	maximumDiarizationThreshold         = 0.4
	minimumTranscriptionTemperature     = 0.0
	maximumTranscriptionTemperature     = 2.0
	minimumTranscriptionSeed            = 0
	maximumKeyterms                     = 1000
	timestampGranularityNone            = "none"
	timestampGranularityWord            = "word"
	timestampGranularityCharacter       = "character"
	transcriptionFileFormatOther        = "other"
	transcriptionFileFormatPCM16LE16KHz = "pcm_s16le_16"
)

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

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	baseURL := cmp.Or(config.BaseURL, DefaultBaseURL)
	boundClient, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.HTTPClient, BaseURL: baseURL})
	if err != nil {
		return nil, err
	}
	client := resty.NewWithClient(boundClient).SetResponseBodyLimit(httpclient.DefaultMaxJSONResponseBytes)
	client.SetJSONMarshaler(func(value any) ([]byte, error) { return jsonv2.Marshal(value) }).
		SetJSONUnmarshaler(func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) })
	client.SetBaseURL(baseURL).
		SetHeader("xi-api-key", config.APIKey).
		SetHeader("Accept", "audio/*")

	return &api{http: client}, nil
}

type ttsRequest struct {
	Body                     ttsBody `json:",embed"`
	EnableLogging            *bool   `json:"enable_logging,omitzero"`
	OptimizeStreamingLatency *int    `json:"optimize_streaming_latency,omitzero"`
}

func (t *ttsRequest) UnmarshalJSON(data []byte) error {
	type wireOptions ttsRequest
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("elevenlabs: request options: %w", err)
	}
	if decoded == nil {
		return errors.New("elevenlabs: request options must be an object")
	}
	*t = ttsRequest(*decoded)
	return nil
}

type ttsBody struct {
	Text                            string                           `json:"text"`
	ModelID                         string                           `json:"model_id,omitempty"`
	LanguageCode                    string                           `json:"language_code,omitempty"`
	VoiceSettings                   *voiceSettings                   `json:"voice_settings,omitzero"`
	Seed                            *int64                           `json:"seed,omitzero"`
	PreviousText                    string                           `json:"previous_text,omitempty"`
	NextText                        string                           `json:"next_text,omitempty"`
	PreviousRequestIDs              []string                         `json:"previous_request_ids,omitzero"`
	NextRequestIDs                  []string                         `json:"next_request_ids,omitzero"`
	ApplyTextNormalization          string                           `json:"apply_text_normalization,omitempty"`
	ApplyLanguageTextNormalization  *bool                            `json:"apply_language_text_normalization,omitzero"`
	PronunciationDictionaryLocators []pronunciationDictionaryLocator `json:"pronunciation_dictionary_locators,omitzero"`
}

type pronunciationDictionaryLocator struct {
	PronunciationDictionaryID string `json:"pronunciation_dictionary_id"`
	VersionID                 string `json:"version_id,omitempty"`
}

type voiceSettings struct {
	Stability       *float64 `json:"stability,omitzero"`
	SimilarityBoost *float64 `json:"similarity_boost,omitzero"`
	Style           *float64 `json:"style,omitzero"`
	UseSpeakerBoost *bool    `json:"use_speaker_boost,omitzero"`
	Speed           *float64 `json:"speed,omitzero"`
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
		if body := resp.RawBody(); body != nil {
			_ = body.Close()
		}
		return nil, nil, fmt.Errorf("elevenlabs: stream: %w", httpclient.NewResponseError(resp.StatusCode(), ""))
	}
	return resp.RawBody(), resp.Header(), nil
}

func (a *api) buildAudioRequest(ctx context.Context, outputFormat string, body *ttsRequest) *resty.Request {
	req := a.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(&body.Body)
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

// Asynchronous and multi-result options cannot honor Core's transcription contract.
type transcriptionRequest struct {
	ModelID               string   `json:"model_id,omitempty"`
	LanguageCode          string   `json:"language_code,omitempty"`
	Diarize               *bool    `json:"diarize,omitzero"`
	NumSpeakers           *int     `json:"num_speakers,omitzero"`
	TagAudioEvents        *bool    `json:"tag_audio_events,omitzero"`
	TimestampsGranularity string   `json:"timestamps_granularity,omitempty"`
	DiarizationThreshold  *float64 `json:"diarization_threshold,omitzero"`
	FileFormat            string   `json:"file_format,omitempty"`
	Temperature           *float64 `json:"temperature,omitzero"`
	Seed                  *int     `json:"seed,omitzero"`
	NoVerbatim            *bool    `json:"no_verbatim,omitzero"`
	UseSpeakerLibrary     *bool    `json:"use_speaker_library,omitzero"`
	DetectSpeakerRoles    *bool    `json:"detect_speaker_roles,omitzero"`
	Keyterms              []string `json:"keyterms,omitzero"`
}

func (t *transcriptionRequest) UnmarshalJSON(data []byte) error {
	type wireOptions transcriptionRequest
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("elevenlabs: request options: %w", err)
	}
	if decoded == nil {
		return errors.New("elevenlabs: request options must be an object")
	}
	*t = transcriptionRequest(*decoded)
	return nil
}

func (t *transcriptionRequest) form() url.Values {
	form := make(url.Values)
	if t.ModelID != "" {
		form.Set("model_id", t.ModelID)
	}
	if t.LanguageCode != "" {
		form.Set("language_code", t.LanguageCode)
	}
	if t.Diarize != nil {
		form.Set("diarize", strconv.FormatBool(*t.Diarize))
	}
	if t.NumSpeakers != nil {
		form.Set("num_speakers", strconv.Itoa(*t.NumSpeakers))
	}
	if t.TagAudioEvents != nil {
		form.Set("tag_audio_events", strconv.FormatBool(*t.TagAudioEvents))
	}
	if t.TimestampsGranularity != "" {
		form.Set("timestamps_granularity", t.TimestampsGranularity)
	}
	if t.DiarizationThreshold != nil {
		form.Set("diarization_threshold", strconv.FormatFloat(*t.DiarizationThreshold, 'f', -1, 64))
	}
	if t.FileFormat != "" {
		form.Set("file_format", t.FileFormat)
	}
	if t.Temperature != nil {
		form.Set("temperature", strconv.FormatFloat(*t.Temperature, 'f', -1, 64))
	}
	if t.Seed != nil {
		form.Set("seed", strconv.Itoa(*t.Seed))
	}
	if t.NoVerbatim != nil {
		form.Set("no_verbatim", strconv.FormatBool(*t.NoVerbatim))
	}
	if t.UseSpeakerLibrary != nil {
		form.Set("use_speaker_library", strconv.FormatBool(*t.UseSpeakerLibrary))
	}
	if t.DetectSpeakerRoles != nil {
		form.Set("detect_speaker_roles", strconv.FormatBool(*t.DetectSpeakerRoles))
	}
	for _, keyterm := range t.Keyterms {
		form.Add("keyterms", keyterm)
	}
	return form
}

func (t *transcriptionRequest) validate() error {
	if t.NumSpeakers != nil && (*t.NumSpeakers < minimumTranscriptionSpeakers || *t.NumSpeakers > maximumTranscriptionSpeakers) {
		return fmt.Errorf("elevenlabs: num_speakers must be between %d and %d, got %d", minimumTranscriptionSpeakers, maximumTranscriptionSpeakers, *t.NumSpeakers)
	}
	if t.DiarizationThreshold != nil && (*t.DiarizationThreshold < minimumDiarizationThreshold || *t.DiarizationThreshold > maximumDiarizationThreshold) {
		return fmt.Errorf("elevenlabs: diarization_threshold must be between %g and %g, got %g", minimumDiarizationThreshold, maximumDiarizationThreshold, *t.DiarizationThreshold)
	}
	if t.Temperature != nil && (*t.Temperature < minimumTranscriptionTemperature || *t.Temperature > maximumTranscriptionTemperature) {
		return fmt.Errorf("elevenlabs: transcription temperature must be between %g and %g, got %g", minimumTranscriptionTemperature, maximumTranscriptionTemperature, *t.Temperature)
	}
	if t.Seed != nil && *t.Seed < minimumTranscriptionSeed {
		return fmt.Errorf("elevenlabs: transcription seed must be non-negative, got %d", *t.Seed)
	}
	if t.TimestampsGranularity != "" && t.TimestampsGranularity != timestampGranularityNone && t.TimestampsGranularity != timestampGranularityWord && t.TimestampsGranularity != timestampGranularityCharacter {
		return fmt.Errorf("elevenlabs: timestamps_granularity must be %s, %s, or %s, got %q", timestampGranularityNone, timestampGranularityWord, timestampGranularityCharacter, t.TimestampsGranularity)
	}
	if t.FileFormat != "" && t.FileFormat != transcriptionFileFormatOther && t.FileFormat != transcriptionFileFormatPCM16LE16KHz {
		return fmt.Errorf("elevenlabs: file_format must be %s or %s, got %q", transcriptionFileFormatOther, transcriptionFileFormatPCM16LE16KHz, t.FileFormat)
	}
	if len(t.Keyterms) > maximumKeyterms {
		return fmt.Errorf("elevenlabs: keyterms must contain at most %d entries, got %d", maximumKeyterms, len(t.Keyterms))
	}
	if t.ModelID == ModelScribeV1 && len(t.Keyterms) > 0 {
		return errors.New("elevenlabs: scribe_v1 does not support keyterms")
	}
	return nil
}

type transcriptionResponse struct {
	LanguageCode        string                     `json:"language_code"`
	LanguageProbability *float64                   `json:"language_probability"`
	Text                *string                    `json:"text"`
	Words               []json.RawMessage          `json:"words"`
	Entities            []json.RawMessage          `json:"entities"`
	Raw                 map[string]json.RawMessage `json:"-"`
}

func (a *api) transcription(ctx context.Context, audio []byte, mimeType string, req *transcriptionRequest) (*transcriptionResponse, error) {
	if len(audio) == 0 {
		return nil, errors.New("elevenlabs: transcription audio must not be empty")
	}
	var out transcriptionResponse
	r := a.http.R().
		SetContext(ctx).
		SetMultipartField("file", "audio", cmp.Or(mimeType, "application/octet-stream"), bytes.NewReader(audio)).
		SetFormDataFromValues(req.form()).
		SetResult(&out)
	resp, err := r.Post("/speech-to-text")
	if err != nil {
		return nil, fmt.Errorf("elevenlabs: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("elevenlabs: %w", httpclient.NewResponseError(resp.StatusCode(), ""))
	}
	if err := jsonv2.Unmarshal(resp.Body(), &out.Raw); err != nil {
		return nil, fmt.Errorf("elevenlabs: decode response facts: %w", err)
	}
	return &out, nil
}
