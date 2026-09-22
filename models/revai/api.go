package revai

import (
	"bytes"
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"
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

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	client := resty.New()
	if config.HTTPClient != nil {
		client = resty.NewWithClient(config.HTTPClient)
	}
	client.SetBaseURL(cmp.Or(config.BaseURL, DefaultBaseURL)).
		SetAuthToken(config.APIKey)
	return &api{http: client}, nil
}

type jobOptions struct {
	MediaURL             string         `json:"media_url,omitempty"`
	SourceConfig         map[string]any `json:"source_config,omitzero"`
	Metadata             string         `json:"metadata,omitempty"`
	CallbackURL          string         `json:"callback_url,omitempty"`
	NotificationConfig   map[string]any `json:"notification_config,omitzero"`
	SkipDiarization      bool           `json:"skip_diarization,omitzero"`
	SkipPunctuation      bool           `json:"skip_punctuation,omitzero"`
	RemoveDisfluencies   bool           `json:"remove_disfluencies,omitzero"`
	RemoveAtmospherics   bool           `json:"remove_atmospherics,omitzero"`
	FilterProfanity      bool           `json:"filter_profanity,omitzero"`
	SpeakerChannelsCount int            `json:"speaker_channels_count,omitzero"`
	Speakers             map[string]any `json:"speakers,omitzero"`
	DiarizationType      string         `json:"diarization_type,omitempty"`
	CustomVocabularyID   string         `json:"custom_vocabulary_id,omitempty"`
	CustomVocabularies   []any          `json:"custom_vocabularies,omitzero"`
	Language             string         `json:"language,omitempty"`
	Transcriber          string         `json:"transcriber,omitempty"`
	VerbatimMode         bool           `json:"verbatim,omitzero"`
	RushMode             bool           `json:"rush,omitzero"`
	TestMode             bool           `json:"test_mode,omitzero"`
	SegmentsToTranscribe []any          `json:"segments_to_transcribe,omitzero"`
}

// Job is the body Rev returns for /jobs (and the poll body for
// /jobs/{id}). Status moves through "in_progress" / "transcribed" /
// "failed".
type job struct {
	ID              string    `json:"id"`
	Status          jobStatus `json:"status"`
	CreatedOn       string    `json:"created_on"`
	CompletedOn     string    `json:"completed_on"`
	FailureReason   string    `json:"failure_detail"`
	DurationSeconds float64   `json:"duration_seconds"`
	Language        string    `json:"language"`
}

func (a *api) submitURL(ctx context.Context, opts jobOptions) (*job, error) {
	var out job
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader("Content-Type", mediaTypeJSON).
		SetBody(opts).
		SetResult(&out).
		Post(jobsPath)
	if err != nil {
		return nil, fmt.Errorf("revai: submit failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("revai: http %d: %s", resp.StatusCode(), resp.String())
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
	return &out, nil
}

func (a *api) getJob(ctx context.Context, id string) (*job, error) {
	var out job
	resp, err := a.http.R().SetContext(ctx).SetResult(&out).Get(jobsPath + "/" + id)
	if err != nil {
		return nil, fmt.Errorf("revai: get job failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("revai: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}

// getTranscriptText fetches the plain-text transcript for a finished
// job. Rev returns 404 until the job reaches "transcribed".
func (a *api) getTranscriptText(ctx context.Context, id string) (string, error) {
	resp, err := a.http.R().
		SetContext(ctx).
		SetHeader("Accept", mediaTypeText).
		Get(jobsPath + "/" + id + "/transcript")
	if err != nil {
		return "", fmt.Errorf("revai: transcript fetch failed: %w", err)
	}
	if !resp.IsSuccess() {
		return "", fmt.Errorf("revai: http %d: %s", resp.StatusCode(), resp.String())
	}
	return resp.String(), nil
}
