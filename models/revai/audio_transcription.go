package revai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
)

// AudioTranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type AudioTranscriptionModelConfig struct {
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
	PollInterval   time.Duration
	PollTimeout    time.Duration
}

func (a AudioTranscriptionModelConfig) Validate() error {
	if a.APIKey == "" {
		return errors.New("revai: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("revai: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	if a.PollInterval < 0 {
		return errors.New("revai: PollInterval must not be negative")
	}
	if a.PollTimeout < 0 {
		return errors.New("revai: PollTimeout must not be negative")
	}
	return nil
}

var _ transcription.Model = (*AudioTranscriptionModel)(nil)

// AudioTranscriptionModel wraps Rev AI's async transcription flow.
// Rev is async-only: Call submits the audio, polls /jobs/{id} until
// "transcribed", then fetches the plain-text transcript.
//
// Diarization, custom vocabularies, profanity filtering, language
// hints and transcriber selection (machine vs human) all live on the
// extension-threaded [JobOptions].
type AudioTranscriptionModel struct {
	api            *api
	defaultOptions transcription.Options
	pollInterval   time.Duration
	pollTimeout    time.Duration
}

// NewAudioTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewAudioTranscriptionModel(config AudioTranscriptionModelConfig) (*AudioTranscriptionModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
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
	return &AudioTranscriptionModel{api: api, defaultOptions: config.DefaultOptions.Clone(), pollInterval: pi, pollTimeout: pt}, nil
}

func (a *AudioTranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	jobOptsValue, _, err := effectiveOptions.Extensions.Decode[jobOptions](RequestExtensionKey)
	jobOpts := &jobOptsValue
	if err != nil {
		return nil, err
	}
	if jobOpts.Language == "" && effectiveOptions.Language != "" {
		jobOpts.Language = effectiveOptions.Language
	}
	jobOpts.Transcriber = effectiveOptions.Model
	if jobOpts.Transcriber != ModelMachine && jobOpts.Transcriber != ModelHuman {
		return nil, fmt.Errorf("revai: transcription model must be %q or %q, got %q", ModelMachine, ModelHuman, jobOpts.Transcriber)
	}

	var job *job
	if jobOpts.MediaURL != "" {
		job, err = a.api.submitURL(ctx, *jobOpts)
	} else {
		audio, audioErr := req.Audio.Bytes()
		if audioErr != nil {
			return nil, audioErr
		}
		job, err = a.api.upload(ctx, audio, req.Audio.MIME, *jobOpts)
	}
	if err != nil {
		return nil, err
	}

	final, err := a.pollUntilDone(ctx, job.ID)
	if err != nil {
		return nil, err
	}

	text, err := a.api.getTranscriptText(ctx, final.ID)
	if err != nil {
		return nil, err
	}

	var outputMetadata metadata.Map
	if final.Language != "" {
		if setErr := outputMetadata.Set("revai/language", final.Language); setErr != nil {
			return nil, setErr
		}
	}
	if final.DurationSeconds > 0 {
		if setErr := outputMetadata.Set("revai/duration_seconds", final.DurationSeconds); setErr != nil {
			return nil, setErr
		}
	}

	output, err := transcription.NewOutput(text, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &transcription.ResponseMetadata{Model: jobOpts.Transcriber}
	if err := meta.Extra.Set("revai/job_id", final.ID); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(ResponseExtensionKey, final); err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, meta)
}

func (a *AudioTranscriptionModel) pollUntilDone(ctx context.Context, id string) (*job, error) {
	deadline, cancel := context.WithTimeout(ctx, a.pollTimeout)
	defer cancel()
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		resp, err := a.api.getJob(deadline, id)
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
