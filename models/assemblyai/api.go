package assemblyai

import (
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
		return errors.New("assemblyai: APIKey is required")
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
		SetHeader("Authorization", config.APIKey)

	return &api{http: client}, nil
}

type uploadResponse struct {
	UploadURL string `json:"upload_url"`
}

type transcriptRequest struct {
	AudioURL                    string         `json:"audio_url"`
	SpeechModels                []string       `json:"speech_models"`
	LanguageCode                string         `json:"language_code,omitempty"`
	LanguageCodes               []string       `json:"language_codes,omitzero"`
	LanguageDetection           *bool          `json:"language_detection,omitempty"`
	LanguageConfidenceThreshold *float64       `json:"language_confidence_threshold,omitempty"`
	Punctuate                   *bool          `json:"punctuate,omitempty"`
	FormatText                  *bool          `json:"format_text,omitempty"`
	SpeakerLabels               *bool          `json:"speaker_labels,omitempty"`
	SpeakersExpected            *int           `json:"speakers_expected,omitempty"`
	SentimentAnalysis           *bool          `json:"sentiment_analysis,omitempty"`
	EntityDetection             *bool          `json:"entity_detection,omitempty"`
	IABCategories               *bool          `json:"iab_categories,omitempty"`
	AutoHighlights              *bool          `json:"auto_highlights,omitempty"`
	ContentSafety               *bool          `json:"content_safety,omitempty"`
	ContentSafetyConfidence     *int           `json:"content_safety_confidence,omitempty"`
	Disfluencies                *bool          `json:"disfluencies,omitempty"`
	FilterProfanity             *bool          `json:"filter_profanity,omitempty"`
	Multichannel                *bool          `json:"multichannel,omitempty"`
	Prompt                      string         `json:"prompt,omitempty"`
	KeytermsPrompt              []string       `json:"keyterms_prompt,omitzero"`
	Domain                      string         `json:"domain,omitempty"`
	RedactPII                   *bool          `json:"redact_pii,omitempty"`
	RedactPIIPolicies           []string       `json:"redact_pii_policies,omitzero"`
	SpeechThreshold             *float64       `json:"speech_threshold,omitempty"`
	AudioStartFrom              *int           `json:"audio_start_from,omitempty"`
	AudioEndAt                  *int           `json:"audio_end_at,omitempty"`
	WebhookURL                  string         `json:"webhook_url,omitempty"`
	WebhookAuthHeaderName       string         `json:"webhook_auth_header_name,omitempty"`
	SpeechUnderstanding         map[string]any `json:"speech_understanding,omitzero"`
}

func (t *transcriptRequest) validate() error {
	for index, model := range t.SpeechModels {
		if model != ModelUniversal3Point5Pro && model != ModelUniversal2 {
			return fmt.Errorf("assemblyai: speech_models[%d] must be %q or %q, got %q", index, ModelUniversal3Point5Pro, ModelUniversal2, model)
		}
	}
	if t.Prompt != "" && len(t.KeytermsPrompt) > 0 {
		return errors.New("assemblyai: prompt and keyterms_prompt are mutually exclusive")
	}
	if t.LanguageConfidenceThreshold != nil && (*t.LanguageConfidenceThreshold < 0 || *t.LanguageConfidenceThreshold > 1) {
		return fmt.Errorf("assemblyai: language_confidence_threshold must be between 0 and 1, got %g", *t.LanguageConfidenceThreshold)
	}
	if t.SpeechThreshold != nil && (*t.SpeechThreshold < 0 || *t.SpeechThreshold > 1) {
		return fmt.Errorf("assemblyai: speech_threshold must be between 0 and 1, got %g", *t.SpeechThreshold)
	}
	if t.ContentSafetyConfidence != nil && (*t.ContentSafetyConfidence < 25 || *t.ContentSafetyConfidence > 100) {
		return fmt.Errorf("assemblyai: content_safety_confidence must be between 25 and 100, got %d", *t.ContentSafetyConfidence)
	}
	return nil
}

// transcriptStatus enumerates the four values AssemblyAI documents for
// [transcriptResponse].Status. Only queued and processing are still moving.
type transcriptStatus string

const (
	statusQueued     transcriptStatus = "queued"
	statusProcessing transcriptStatus = "processing"
	statusCompleted  transcriptStatus = "completed"
	statusErrored    transcriptStatus = "error"
)

type transcriptResponse struct {
	ID                 string           `json:"id"`
	Status             transcriptStatus `json:"status"`
	Text               string           `json:"text"`
	Confidence         float64          `json:"confidence"`
	AudioDuration      int64            `json:"audio_duration"`
	LanguageCode       string           `json:"language_code"`
	LanguageConfidence float64          `json:"language_confidence"`
	SpeechModelUsed    string           `json:"speech_model_used"`
	Error              string           `json:"error"`
	Utterances         []struct {
		Start      int64   `json:"start"`
		End        int64   `json:"end"`
		Speaker    string  `json:"speaker"`
		Text       string  `json:"text"`
		Confidence float64 `json:"confidence"`
	} `json:"utterances"`
	Words []struct {
		Text       string  `json:"text"`
		Start      int64   `json:"start"`
		End        int64   `json:"end"`
		Confidence float64 `json:"confidence"`
		Speaker    string  `json:"speaker"`
	} `json:"words"`
	Raw map[string]any `json:"-"`
}

func (a *api) upload(ctx context.Context, audio []byte) (*uploadResponse, error) {
	if len(audio) == 0 {
		return nil, errors.New("assemblyai: upload audio must not be empty")
	}

	var out uploadResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/octet-stream").
		SetBody(audio).
		SetResult(&out).
		Post("/upload")
	if err != nil {
		return nil, fmt.Errorf("assemblyai: upload failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("assemblyai: upload http %d: %s", resp.StatusCode(), resp.String())
	}
	if out.UploadURL == "" {
		return nil, errors.New("assemblyai: upload response omitted upload_url")
	}
	return &out, nil
}

func (a *api) createTranscript(ctx context.Context, req *transcriptRequest) (*transcriptResponse, error) {
	if req == nil {
		return nil, errors.New("assemblyai: request must not be nil")
	}

	var out transcriptResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(req).
		SetResult(&out).
		Post("/transcript")
	if err != nil {
		return nil, fmt.Errorf("assemblyai: create transcript failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("assemblyai: http %d: %s", resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out.Raw); err != nil {
		return nil, fmt.Errorf("assemblyai: preserve create response: %w", err)
	}
	return &out, nil
}

func (a *api) get(ctx context.Context, id string) (*transcriptResponse, error) {
	if id == "" {
		return nil, errors.New("assemblyai: transcript id must not be empty")
	}
	var out transcriptResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetResult(&out).
		Get("/transcript/" + id)
	if err != nil {
		return nil, fmt.Errorf("assemblyai: get transcript failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("assemblyai: http %d: %s", resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out.Raw); err != nil {
		return nil, fmt.Errorf("assemblyai: preserve transcript response: %w", err)
	}
	return &out, nil
}
