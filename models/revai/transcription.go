package revai

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
)

type TranscriptionModelConfig struct {
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
	PollInterval   time.Duration
	PollTimeout    time.Duration
}

func (t TranscriptionModelConfig) Validate() error {
	if t.APIKey == "" {
		return errors.New("revai: APIKey is required")
	}
	if t.DefaultOptions.Model == "" {
		return errors.New("revai: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	if t.PollInterval < 0 {
		return errors.New("revai: PollInterval must not be negative")
	}
	if t.PollTimeout < 0 {
		return errors.New("revai: PollTimeout must not be negative")
	}
	return nil
}

var _ transcription.Model = (*TranscriptionModel)(nil)

// TranscriptionModel wraps Rev AI's async transcription flow.
// Rev is async-only: Call submits the audio, polls /jobs/{id} until
// "transcribed", then fetches the plain-text transcript.
//
// Language and transcriber selection use transcription.Options.Language and
// transcription.Options.Model (ModelMachine or ModelHuman). Diarization, custom
// vocabularies, and profanity filtering use Options.Extensions under
// RequestExtensionKey.
// Audio.Source alone chooses the media location. URI sources use source_config.url;
// the native source_config extension may supply auth_headers only, for URI audio.
type TranscriptionModel struct {
	api            *api
	defaultOptions transcription.Options
	pollInterval   time.Duration
	pollTimeout    time.Duration
}

func NewTranscriptionModel(ctx context.Context, config TranscriptionModelConfig) (*TranscriptionModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}
	pi := config.PollInterval
	if pi == 0 {
		pi = DefaultPollInterval
	}
	pt := config.PollTimeout
	if pt == 0 {
		pt = DefaultPollTimeout
	}
	return &TranscriptionModel{api: api, defaultOptions: config.DefaultOptions.Clone(), pollInterval: pi, pollTimeout: pt}, nil
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	effectiveOptions, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	nativeFields, _, decodeErr := effectiveOptions.Extensions.Decode[map[string]json.RawMessage](RequestExtensionKey)
	if decodeErr != nil {
		return nil, decodeErr
	}
	for _, field := range []string{fieldMediaURL, fieldLanguage, fieldTranscriber} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("revai: extension %q field %q is owned by Core", RequestExtensionKey, field)
		}
	}
	if raw, exists := nativeFields[fieldSourceConfig]; exists {
		var sourceFields map[string]json.RawMessage
		if decodeErr := jsonv2.Unmarshal(raw, &sourceFields); decodeErr != nil {
			return nil, fmt.Errorf("revai: source_config: %w", decodeErr)
		}
		for field := range sourceFields {
			if field != fieldAuthHeaders {
				return nil, fmt.Errorf("revai: source_config field %q is not a native authorization option; Audio.Source owns the URL", field)
			}
		}
	}

	jobOptsValue, _, err := effectiveOptions.Extensions.Decode[jobOptions](RequestExtensionKey)
	jobOpts := &jobOptsValue
	if err != nil {
		return nil, err
	}
	jobOpts.Language = effectiveOptions.Language
	jobOpts.Transcriber = effectiveOptions.Model
	if jobOpts.Transcriber != ModelMachine && jobOpts.Transcriber != ModelHuman {
		return nil, fmt.Errorf("revai: transcription model must be %q or %q, got %q", ModelMachine, ModelHuman, jobOpts.Transcriber)
	}

	var job *job
	if req.Audio.Source.Kind == media.SourceURI {
		if jobOpts.SourceConfig == nil {
			jobOpts.SourceConfig = &sourceConfig{}
		}
		jobOpts.SourceConfig.URL = req.Audio.Source.URI
		job, err = t.api.submitURL(ctx, *jobOpts)
	} else {
		if jobOpts.SourceConfig != nil {
			return nil, errors.New("revai: source_config requires URI audio")
		}
		audio, audioErr := req.Audio.Bytes()
		if audioErr != nil {
			return nil, audioErr
		}
		job, err = t.api.upload(ctx, audio, req.Audio.MIME, *jobOpts)
	}
	if err != nil {
		return nil, err
	}

	final, err := t.pollUntilDone(ctx, job.ID)
	if err != nil {
		return nil, err
	}

	text, err := t.api.getTranscriptText(ctx, final.ID)
	if err != nil {
		return nil, err
	}

	var outputMetadata metadata.Map
	if final.Language != "" {
		if setErr := outputMetadata.Set(metadataLanguage, final.Language); setErr != nil {
			return nil, setErr
		}
	}
	if final.DurationSeconds > 0 {
		if setErr := outputMetadata.Set(metadataDurationSeconds, final.DurationSeconds); setErr != nil {
			return nil, setErr
		}
	}

	output, err := transcription.NewOutput(text, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &transcription.ResponseMetadata{Model: jobOpts.Transcriber}
	if err := meta.Extra.Set(metadataJobID, final.ID); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(ResponseExtensionKey, final); err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, meta)
}

func (t *TranscriptionModel) pollUntilDone(ctx context.Context, id string) (*job, error) {
	deadline, cancel := context.WithTimeout(ctx, t.pollTimeout)
	defer cancel()
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()
	for {
		resp, err := t.api.getJob(deadline, id)
		if err != nil {
			return nil, err
		}
		switch resp.Status {
		case jobStatusTranscribed:
			return resp, nil
		case jobStatusInProgress:
			// The only state Rev.ai documents as still moving.
		case jobStatusFailed:
			return nil, fmt.Errorf("revai: transcription failed: %s", resp.FailureReason)
		default:
			// Continuing to poll is only safe for a state known to advance, so
			// an unrecognized one is reported rather than absorbed: an added
			// end state would otherwise arrive as this call's own timeout,
			// whose obvious remedies are both wrong.
			return nil, fmt.Errorf("revai: job %s reports unrecognized status %q", id, resp.Status)
		}
		select {
		case <-deadline.Done():
			return nil, deadline.Err()
		case <-ticker.C:
		}
	}
}
