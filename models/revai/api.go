package revai

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/transcription"
)

const (
	jobsPath              = "/jobs"
	mediaPartName         = "media"
	optionsPartName       = "options"
	mediaTypeJSON         = "application/json"
	mediaTypeBinary       = "application/octet-stream"
	mediaTypeText         = "text/plain"
	defaultUploadFileName = "audio"
)

type jobStatus string

const (
	jobStatusInProgress  jobStatus = "in_progress"
	jobStatusTranscribed jobStatus = "transcribed"
	jobStatusFailed      jobStatus = "failed"
)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("revai: APIKey is required")
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
		return nil, fmt.Errorf("revai: HTTP client: %w", err)
	}
	client := resty.NewWithClient(boundClient)
	client.SetJSONMarshaler(func(value any) ([]byte, error) { return jsonv2.Marshal(value) }).
		SetJSONUnmarshaler(func(data []byte, value any) error { return jsonv2.Unmarshal(data, value) })
	client.SetBaseURL(baseURL).
		SetAuthToken(config.APIKey)
	return &api{http: client}, nil
}

type jobOptions struct {
	SourceConfig         *sourceConfig              `json:"source_config,omitzero"`
	Metadata             string                     `json:"metadata,omitempty"`
	CallbackURL          string                     `json:"callback_url,omitempty"`
	NotificationConfig   map[string]json.RawMessage `json:"notification_config,omitzero"`
	SkipDiarization      bool                       `json:"skip_diarization,omitzero"`
	SkipPunctuation      bool                       `json:"skip_punctuation,omitzero"`
	RemoveDisfluencies   bool                       `json:"remove_disfluencies,omitzero"`
	RemoveAtmospherics   bool                       `json:"remove_atmospherics,omitzero"`
	FilterProfanity      bool                       `json:"filter_profanity,omitzero"`
	SpeakerChannelsCount int                        `json:"speaker_channels_count,omitzero"`
	Speakers             map[string]json.RawMessage `json:"speakers,omitzero"`
	DiarizationType      string                     `json:"diarization_type,omitempty"`
	CustomVocabularyID   string                     `json:"custom_vocabulary_id,omitempty"`
	CustomVocabularies   []json.RawMessage          `json:"custom_vocabularies,omitzero"`
	Language             string                     `json:"language,omitempty"`
	Transcriber          string                     `json:"transcriber,omitempty"`
	VerbatimMode         *bool                      `json:"verbatim,omitzero"`
	RushMode             bool                       `json:"rush,omitzero"`
	TestMode             bool                       `json:"test_mode,omitzero"`
	SegmentsToTranscribe []json.RawMessage          `json:"segments_to_transcribe,omitzero"`
}

func (j *jobOptions) UnmarshalJSON(data []byte) error {
	type wireOptions jobOptions
	var decoded *wireOptions
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("revai: transcription options: %w", err)
	}
	if decoded == nil {
		return errors.New("revai: transcription options must be an object")
	}
	*j = jobOptions(*decoded)
	return nil
}

type sourceConfig struct {
	URL         string            `json:"url"`
	AuthHeaders map[string]string `json:"auth_headers,omitzero"`
}

type job struct {
	ID              string    `json:"id"`
	Status          jobStatus `json:"status"`
	CreatedOn       string    `json:"created_on"`
	CompletedOn     string    `json:"completed_on"`
	FailureReason   string    `json:"failure_detail"`
	DurationSeconds float64   `json:"duration_seconds"`
	Language        string    `json:"language"`
}

func (j *job) validateIdentity(expected string) error {
	if j.ID == "" {
		return fmt.Errorf("%w: revai: job requires id", transcription.ErrInvalidResponse)
	}
	if expected != "" && j.ID != expected {
		return fmt.Errorf("%w: revai: polled job id %q differs from submitted id %q", transcription.ErrInvalidResponse, j.ID, expected)
	}
	return nil
}

func (a *api) submitURL(ctx context.Context, opts jobOptions) (*job, error) {
	var out job
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader(headerContentType, mediaTypeJSON).
		SetBody(opts).
		SetResult(&out).
		Post(jobsPath)
	if err != nil {
		return nil, fmt.Errorf("revai: submit failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("revai: http %d: %s", resp.StatusCode(), resp.String())
	}
	if err := out.validateIdentity(""); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *api) upload(ctx context.Context, audio []byte, mimeType string, opts jobOptions) (*job, error) {
	if len(audio) == 0 {
		return nil, errors.New("revai: upload audio must not be empty")
	}
	optsJSON, err := jsonv2.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("revai: encode job options: %w", err)
	}

	var out job
	resp, err := a.http.R().
		SetContext(ctx).
		SetMultipartField(mediaPartName, defaultUploadFileName, cmp.Or(mimeType, mediaTypeBinary), bytes.NewReader(audio)).
		SetMultipartField(optionsPartName, "", mediaTypeJSON, bytes.NewReader(optsJSON)).
		SetResult(&out).
		Post(jobsPath)
	if err != nil {
		return nil, fmt.Errorf("revai: upload failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("revai: http %d: %s", resp.StatusCode(), resp.String())
	}
	if err := out.validateIdentity(""); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *api) getJob(ctx context.Context, id string) (*job, error) {
	var out job
	resp, err := a.http.R().SetContext(ctx).SetResult(&out).Get(jobsPath + "/" + url.PathEscape(id))
	if err != nil {
		return nil, fmt.Errorf("revai: get job failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("revai: http %d: %s", resp.StatusCode(), resp.String())
	}
	if err := out.validateIdentity(id); err != nil {
		return nil, err
	}
	return &out, nil
}

// Rev returns 404 until the job reaches "transcribed".
func (a *api) getTranscriptText(ctx context.Context, id string) (string, error) {
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader(headerAccept, mediaTypeText).
		Get(jobsPath + "/" + url.PathEscape(id) + pathTranscriptSuffix)
	if err != nil {
		return "", fmt.Errorf("revai: transcript fetch failed: %w", err)
	}
	if !resp.IsSuccess() {
		return "", fmt.Errorf("revai: http %d: %s", resp.StatusCode(), resp.String())
	}
	return resp.String(), nil
}
