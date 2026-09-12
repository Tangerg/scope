package deepgram

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-resty/resty/v2"
)

type apiConfig struct {
	APIKey           string
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("deepgram: APIKey is required")
	}
	return nil
}

type api struct {
	http             *resty.Client
	maxResponseBytes int64
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
		SetHeader("Authorization", "Token "+config.APIKey).
		SetResponseBodyLimit(int(config.MaxResponseBytes))

	return &api{http: client, maxResponseBytes: config.MaxResponseBytes}, nil
}

// ListenParams holds current query-string options for Deepgram /listen.
// Extra is available for newly released official query parameters that have
// not yet acquired a typed field.
type listenParams struct {
	Model          string
	Language       string
	Tier           string
	Version        string
	Punctuate      *bool
	SmartFormat    *bool
	Diarize        *bool
	Numerals       *bool
	Paragraphs     *bool
	Utterances     *bool
	Topics         *bool
	Sentiment      *bool
	Intents        *bool
	DetectEntities *bool
	DetectLanguage *bool
	Summarize      string
	Redact         []string
	Keyterms       []string
	Extra          url.Values
}

func (l *listenParams) query() url.Values {
	q := url.Values{}
	if l == nil {
		return q
	}
	for k, vs := range l.Extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	setStr := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	setBool := func(k string, v *bool) {
		if v == nil {
			return
		}
		if *v {
			q.Set(k, "true")
		} else {
			q.Set(k, "false")
		}
	}
	setStr("model", l.Model)
	setStr("language", l.Language)
	setStr("tier", l.Tier)
	setStr("version", l.Version)
	setStr("summarize", l.Summarize)
	setBool("punctuate", l.Punctuate)
	setBool("smart_format", l.SmartFormat)
	setBool("diarize", l.Diarize)
	setBool("numerals", l.Numerals)
	setBool("paragraphs", l.Paragraphs)
	setBool("utterances", l.Utterances)
	setBool("topics", l.Topics)
	setBool("sentiment", l.Sentiment)
	setBool("intents", l.Intents)
	setBool("detect_entities", l.DetectEntities)
	setBool("detect_language", l.DetectLanguage)
	for _, r := range l.Redact {
		q.Add("redact", r)
	}
	for _, keyterm := range l.Keyterms {
		q.Add("keyterm", keyterm)
	}
	return q
}

type listenResponse struct {
	RequestID string `json:"request_id"`
	Metadata  struct {
		TransactionKey string   `json:"transaction_key"`
		RequestID      string   `json:"request_id"`
		Channels       int      `json:"channels"`
		Duration       float64  `json:"duration"`
		Models         []string `json:"models"`
		Metadata       map[string]struct {
			Name string `json:"name"`
			Tier string `json:"tier"`
		} `json:"model_info"`
	} `json:"metadata"`
	Results struct {
		Channels []struct {
			Alternatives []struct {
				Transcript string  `json:"transcript"`
				Confidence float64 `json:"confidence"`
				Words      []struct {
					Word    string  `json:"word"`
					Start   float64 `json:"start"`
					End     float64 `json:"end"`
					Speaker int     `json:"speaker,omitempty"`
				} `json:"words"`
			} `json:"alternatives"`
		} `json:"channels"`
		Utterances []struct {
			Start      float64 `json:"start"`
			End        float64 `json:"end"`
			Speaker    int     `json:"speaker"`
			Transcript string  `json:"transcript"`
		} `json:"utterances,omitempty"`
	} `json:"results"`
	Raw map[string]any `json:"-"`
}

func (a *api) listen(ctx context.Context, audio []byte, contentType string, params *listenParams) (*listenResponse, error) {
	if len(audio) == 0 {
		return nil, errors.New("deepgram: transcription audio must not be empty")
	}

	var out listenResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", cmp.Or(contentType, "application/octet-stream")).
		SetQueryParamsFromValues(params.query()).
		SetBody(audio).
		SetResult(&out).
		Post("/listen")
	if err != nil {
		return nil, fmt.Errorf("deepgram: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("deepgram: http %d: %s", resp.StatusCode(), resp.String())
	}
	if err := json.Unmarshal(resp.Body(), &out.Raw); err != nil {
		return nil, fmt.Errorf("deepgram: preserve transcription response: %w", err)
	}
	return &out, nil
}

// SpeakParams holds the query-string knobs Deepgram /speak accepts.
// See https://developers.deepgram.com/reference/text-to-speech-api.
type speakParams struct {
	Model      string // "aura-asteria-en" / "aura-2-thalia-en" etc.
	Encoding   string // "mp3" / "linear16" / "opus" / "flac" / "aac" / "mulaw" / "alaw"
	Container  string // "wav" / "none"
	SampleRate int
	BitRate    int
	Speed      float64
	Extra      url.Values
}

func (s *speakParams) query() url.Values {
	q := url.Values{}
	if s == nil {
		return q
	}
	for k, vs := range s.Extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	if s.Model != "" {
		q.Set("model", s.Model)
	}
	if s.Encoding != "" {
		q.Set("encoding", s.Encoding)
	}
	if s.Container != "" {
		q.Set("container", s.Container)
	}
	if s.SampleRate > 0 {
		q.Set("sample_rate", strconv.Itoa(s.SampleRate))
	}
	if s.BitRate > 0 {
		q.Set("bit_rate", strconv.Itoa(s.BitRate))
	}
	if s.Speed > 0 {
		q.Set("speed", strconv.FormatFloat(s.Speed, 'f', -1, 64))
	}
	return q
}

// speak posts text to /speak and returns the raw audio bytes plus the
// response headers (request id / content-type live there).
func (a *api) speak(ctx context.Context, text string, params *speakParams) ([]byte, http.Header, error) {
	body, headers, err := a.speakStream(ctx, text, params)
	if err != nil {
		return nil, nil, err
	}
	defer body.Close()
	audio, err := readBounded(body, a.maxResponseBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("deepgram: read speech response: %w", err)
	}
	if len(audio) == 0 {
		return nil, nil, errors.New("deepgram: speech response is empty")
	}
	return audio, headers, nil
}

// speakStream posts text to /speak and exposes the response body as it arrives.
func (a *api) speakStream(ctx context.Context, text string, params *speakParams) (io.ReadCloser, http.Header, error) {
	if text == "" {
		return nil, nil, errors.New("deepgram: speech text must not be empty")
	}

	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "audio/*").
		SetQueryParamsFromValues(params.query()).
		SetBody(map[string]string{"text": text}).
		SetDoNotParseResponse(true).
		Post("/speak")
	if err != nil {
		return nil, nil, fmt.Errorf("deepgram: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		defer resp.RawBody().Close()
		body, readErr := readBounded(resp.RawBody(), maximumErrorResponseBytes)
		if readErr != nil {
			return nil, nil, fmt.Errorf("deepgram: http %d; read error response: %w", resp.StatusCode(), readErr)
		}
		return nil, nil, fmt.Errorf("deepgram: http %d: %s", resp.StatusCode(), string(body))
	}
	if resp.RawBody() == nil {
		return nil, nil, errors.New("deepgram: speech response has no body")
	}
	return resp.RawBody(), resp.Header(), nil
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d-byte limit", maxBytes)
	}
	return data, nil
}
