package gladia

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("gladia: APIKey is required")
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
		SetHeader("x-gladia-key", config.APIKey)
	return &api{http: client}, nil
}

type uploadResponse struct {
	AudioURL      string `json:"audio_url"`
	AudioMetadata struct {
		ID            string  `json:"id"`
		AudioDuration float64 `json:"audio_duration"`
		Channels      int     `json:"channels"`
	} `json:"audio_metadata"`
}

type transcriptionRequest struct {
	AudioURL               string          `json:"audio_url"`
	Model                  string          `json:"model,omitempty"`
	LanguageConfig         *languageConfig `json:"language_config,omitempty"`
	CustomVocabulary       any             `json:"custom_vocabulary,omitempty"`
	CustomVocabularyConfig map[string]any  `json:"custom_vocabulary_config,omitzero"`
	Callback               *bool           `json:"callback,omitempty"`
	CallbackConfig         map[string]any  `json:"callback_config,omitzero"`
	Diarization            *bool           `json:"diarization,omitempty"`
	DiarizationConfig      map[string]any  `json:"diarization_config,omitzero"`
	Translation            *bool           `json:"translation,omitempty"`
	TranslationConfig      map[string]any  `json:"translation_config,omitzero"`
	Summarization          *bool           `json:"summarization,omitempty"`
	SummarizationConfig    map[string]any  `json:"summarization_config,omitzero"`
	NamedEntityRecognition *bool           `json:"named_entity_recognition,omitempty"`
	CustomSpelling         *bool           `json:"custom_spelling,omitempty"`
	CustomSpellingConfig   map[string]any  `json:"custom_spelling_config,omitzero"`
	SentimentAnalysis      *bool           `json:"sentiment_analysis,omitempty"`
	AudioToLLM             *bool           `json:"audio_to_llm,omitempty"`
	AudioToLLMConfig       map[string]any  `json:"audio_to_llm_config,omitzero"`
	PIIRedaction           *bool           `json:"pii_redaction,omitempty"`
	PIIRedactionConfig     map[string]any  `json:"pii_redaction_config,omitzero"`
	Subtitles              *bool           `json:"subtitles,omitempty"`
	SubtitlesConfig        map[string]any  `json:"subtitles_config,omitzero"`
	Sentences              *bool           `json:"sentences,omitempty"`
	PunctuationEnhanced    *bool           `json:"punctuation_enhanced,omitempty"`
	CustomMetadata         map[string]any  `json:"custom_metadata,omitzero"`
}

type languageConfig struct {
	Languages     []string `json:"languages,omitzero"`
	CodeSwitching *bool    `json:"code_switching,omitempty"`
}

type transcriptionCreateResponse struct {
	ID        string `json:"id"`
	ResultURL string `json:"result_url"`
}

// transcriptionStatus enumerates the four values Gladia documents for a
// pre-recorded job. Only queued and processing are still moving.
type transcriptionStatus string

const (
	transcriptionStatusQueued     transcriptionStatus = "queued"
	transcriptionStatusProcessing transcriptionStatus = "processing"
	transcriptionStatusDone       transcriptionStatus = "done"
	transcriptionStatusError      transcriptionStatus = "error"
)

// TranscriptionResult is the body of GET /pre-recorded/{id}.
type transcriptionResult struct {
	ID     string              `json:"id"`
	Status transcriptionStatus `json:"status"`
	Result struct {
		Transcription struct {
			FullTranscript string   `json:"full_transcript"`
			Languages      []string `json:"languages,omitzero"`
			Utterances     []any    `json:"utterances,omitzero"`
		} `json:"transcription"`
		Translation   any `json:"translation,omitempty"`
		Summarization any `json:"summarization,omitempty"`
	} `json:"result"`
	ErrorCode string         `json:"error_code,omitempty"`
	Raw       map[string]any `json:"-"`
}

// upload posts raw audio bytes to /upload, returning a Gladia-hosted
// URL the caller passes to /pre-recorded.
func (a *api) upload(ctx context.Context, audio []byte, mimeType string) (*uploadResponse, error) {
	if len(audio) == 0 {
		return nil, errors.New("gladia: upload audio must not be empty")
	}
	var out uploadResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetMultipartField("audio", "audio", cmp.Or(mimeType, "application/octet-stream"), bytes.NewReader(audio)).
		SetResult(&out).
		Post("/upload")
	if err != nil {
		return nil, fmt.Errorf("gladia: upload failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("gladia: upload http %d: %s", resp.StatusCode(), resp.String())
	}
	if out.AudioURL == "" {
		return nil, errors.New("gladia: upload response omitted audio_url")
	}
	return &out, nil
}

func (a *api) createTranscription(ctx context.Context, req *transcriptionRequest) (*transcriptionCreateResponse, error) {
	if req == nil {
		return nil, errors.New("gladia: request must not be nil")
	}
	var out transcriptionCreateResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(req).
		SetResult(&out).
		Post("/pre-recorded")
	if err != nil {
		return nil, fmt.Errorf("gladia: create failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("gladia: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}

func (a *api) getTranscription(ctx context.Context, id string) (*transcriptionResult, error) {
	if id == "" {
		return nil, errors.New("gladia: transcription id must not be empty")
	}
	var out transcriptionResult
	resp, err := a.http.R().SetContext(ctx).SetResult(&out).Get("/pre-recorded/" + id)
	if err != nil {
		return nil, fmt.Errorf("gladia: poll failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("gladia: http %d: %s", resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out.Raw); err != nil {
		return nil, fmt.Errorf("gladia: preserve transcription response: %w", err)
	}
	return &out, nil
}
