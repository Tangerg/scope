package deepgram

import (
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

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	baseURL := cmp.Or(config.BaseURL, DefaultBaseURL)
	boundClient, err := httpclient.New(ctx, httpclient.Config{HTTPClient: config.HTTPClient, BaseURL: baseURL})
	if err != nil {
		return nil, fmt.Errorf("deepgram: HTTP client: %w", err)
	}
	client := resty.NewWithClient(boundClient)
	client.SetJSONMarshaler(func(value any) ([]byte, error) { return jsonv2.Marshal(value) }).
		SetJSONUnmarshaler(func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) })
	client.SetBaseURL(baseURL).
		SetHeader(headerAuthorization, authorizationTokenPrefix+config.APIKey).
		SetResponseBodyLimit(int(config.MaxResponseBytes))

	return &api{http: client, maxResponseBytes: config.MaxResponseBytes}, nil
}

// Extra is available for newly released official query parameters that have
// not yet acquired a typed field.
type listenParams struct {
	Model          string     `json:"model,omitempty"`
	Language       string     `json:"language,omitempty"`
	Tier           string     `json:"tier,omitempty"`
	Version        string     `json:"version,omitempty"`
	Punctuate      *bool      `json:"punctuate,omitzero"`
	SmartFormat    *bool      `json:"smart_format,omitzero"`
	Diarize        *bool      `json:"diarize,omitzero"`
	Numerals       *bool      `json:"numerals,omitzero"`
	Paragraphs     *bool      `json:"paragraphs,omitzero"`
	Utterances     *bool      `json:"utterances,omitzero"`
	Topics         *bool      `json:"topics,omitzero"`
	Sentiment      *bool      `json:"sentiment,omitzero"`
	Intents        *bool      `json:"intents,omitzero"`
	DetectEntities *bool      `json:"detect_entities,omitzero"`
	DetectLanguage *bool      `json:"detect_language,omitzero"`
	Summarize      string     `json:"summarize,omitempty"`
	Redact         []string   `json:"redact,omitzero"`
	Keyterms       []string   `json:"keyterm,omitzero"`
	Extra          url.Values `json:"extra,omitzero"`
}

func (l *listenParams) UnmarshalJSON(data []byte) error {
	type wireOptions listenParams
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("deepgram: request options: %w", err)
	}
	if decoded == nil {
		return errors.New("deepgram: request options must be an object")
	}
	*l = listenParams(*decoded)
	return nil
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
		if v != nil {
			q.Set(k, strconv.FormatBool(*v))
		}
	}
	setStr(fieldModel, l.Model)
	setStr(fieldLanguage, l.Language)
	setStr(fieldTier, l.Tier)
	setStr(fieldVersion, l.Version)
	setStr(fieldSummarize, l.Summarize)
	setBool(fieldPunctuate, l.Punctuate)
	setBool(fieldSmartFormat, l.SmartFormat)
	setBool(fieldDiarize, l.Diarize)
	setBool(fieldNumerals, l.Numerals)
	setBool(fieldParagraphs, l.Paragraphs)
	setBool(fieldUtterances, l.Utterances)
	setBool(fieldTopics, l.Topics)
	setBool(fieldSentiment, l.Sentiment)
	setBool(fieldIntents, l.Intents)
	setBool(fieldDetectEntities, l.DetectEntities)
	setBool(fieldDetectLanguage, l.DetectLanguage)
	for _, r := range l.Redact {
		q.Add(fieldRedact, r)
	}
	for _, keyterm := range l.Keyterms {
		q.Add(fieldKeyterm, keyterm)
	}
	return q
}

type listenResponse struct {
	Metadata struct {
		RequestID string   `json:"request_id"`
		Channels  *int     `json:"channels"`
		Duration  *float64 `json:"duration"`
	} `json:"metadata"`
	Results struct {
		Channels []struct {
			Alternatives []struct {
				Transcript *string           `json:"transcript"`
				Confidence *float64          `json:"confidence"`
				Words      []json.RawMessage `json:"words"`
			} `json:"alternatives"`
		} `json:"channels"`
		Utterances []json.RawMessage `json:"utterances"`
	} `json:"results"`
	Raw map[string]json.RawMessage `json:"-"`
}

func (a *api) listen(ctx context.Context, audio []byte, contentType string, params *listenParams) (*listenResponse, error) {
	if len(audio) == 0 {
		return nil, errors.New("deepgram: transcription audio must not be empty")
	}

	var out listenResponse
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader(headerContentType, cmp.Or(contentType, mediaTypeBinary)).
		SetQueryParamsFromValues(params.query()).
		SetBody(audio).
		SetResult(&out).
		Post(pathListen)
	if err != nil {
		return nil, fmt.Errorf("deepgram: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("deepgram: http %d: %s", resp.StatusCode(), resp.String())
	}
	if err := jsonv2.Unmarshal(resp.Body(), &out.Raw); err != nil {
		return nil, fmt.Errorf("deepgram: preserve transcription response: %w", err)
	}
	return &out, nil
}

type speakParams struct {
	Model      string     `json:"model,omitempty"`
	Encoding   string     `json:"encoding,omitempty"`
	Container  string     `json:"container,omitempty"`
	SampleRate *int       `json:"sample_rate,omitzero"`
	BitRate    *int       `json:"bit_rate,omitzero"`
	Speed      float64    `json:"speed,omitzero"`
	Extra      url.Values `json:"extra,omitzero"`
}

func (s *speakParams) UnmarshalJSON(data []byte) error {
	type wireOptions speakParams
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("deepgram: request options: %w", err)
	}
	if decoded == nil {
		return errors.New("deepgram: request options must be an object")
	}
	*s = speakParams(*decoded)
	return nil
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
		q.Set(fieldModel, s.Model)
	}
	if s.Encoding != "" {
		q.Set(fieldEncoding, s.Encoding)
	}
	if s.Container != "" {
		q.Set(fieldContainer, s.Container)
	}
	if s.SampleRate != nil {
		q.Set(fieldSampleRate, strconv.Itoa(*s.SampleRate))
	}
	if s.BitRate != nil {
		q.Set(fieldBitRate, strconv.Itoa(*s.BitRate))
	}
	if s.Speed > 0 {
		q.Set(fieldSpeed, strconv.FormatFloat(s.Speed, 'f', -1, 64))
	}
	return q
}

func (s *speakParams) validateMediaControls() error {
	if s.SampleRate != nil {
		if *s.SampleRate <= 0 {
			return errors.New("deepgram: speech: sample_rate must be positive when supplied")
		}
		switch s.Encoding {
		case formatMP3, formatOPUS, formatAAC:
			return fmt.Errorf("deepgram: speech: sample_rate is not configurable for %q", s.Encoding)
		}
	}
	if s.BitRate != nil {
		if *s.BitRate <= 0 {
			return errors.New("deepgram: speech: bit_rate must be positive when supplied")
		}
		switch s.Encoding {
		case formatLINEAR16, formatMULAW, formatALAW, formatFLAC:
			return fmt.Errorf("deepgram: speech: bit_rate is not configurable for %q", s.Encoding)
		}
	}
	return nil
}

type speakRequest struct {
	Text string `json:"text"`
}

func (a *api) speakStream(ctx context.Context, text string, params *speakParams) (io.ReadCloser, http.Header, error) {
	if text == "" {
		return nil, nil, errors.New("deepgram: speech text must not be empty")
	}

	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader(headerContentType, mediaTypeJSON).
		SetHeader(headerAccept, mediaTypeAudio).
		SetQueryParamsFromValues(params.query()).
		SetBody(speakRequest{Text: text}).
		SetDoNotParseResponse(true).
		Post(pathSpeak)
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
