package revai

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
)

type TranscriptionModelConfig struct {
	APIKey         string
	DefaultOptions transcription.Options
	BaseURL        string
	HTTPClient     *http.Client
	// Polling bounds the wait for the async Rev.ai job; zero fields
	// select DefaultPollInterval and DefaultPollTimeout.
	Polling invocation.Polling
}

func (t TranscriptionModelConfig) apiConfig() apiConfig {
	return apiConfig{APIKey: t.APIKey, BaseURL: t.BaseURL, HTTPClient: t.HTTPClient}
}

func (t TranscriptionModelConfig) Validate() error {
	if err := t.apiConfig().validate(); err != nil {
		return err
	}
	if t.DefaultOptions.Model == "" {
		return errors.New("revai: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	if err := t.Polling.Validate(); err != nil {
		return fmt.Errorf("revai: Polling: %w", err)
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
	polling        invocation.Polling
}

func NewTranscriptionModel(ctx context.Context, config TranscriptionModelConfig) (*TranscriptionModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(ctx, config.apiConfig())
	if err != nil {
		return nil, err
	}
	return &TranscriptionModel{api: api, defaultOptions: config.DefaultOptions.Clone(), polling: config.Polling.Or(invocation.Polling{Interval: DefaultPollInterval, Timeout: DefaultPollTimeout})}, nil
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	jobOpts, err := t.buildJobOptions(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	var submitted *job
	if req.Audio.Source.Kind == media.SourceURI {
		if jobOpts.SourceConfig == nil {
			jobOpts.SourceConfig = &sourceConfig{}
		}
		jobOpts.SourceConfig.URL = req.Audio.Source.URI
		submitted, err = t.api.submitURL(ctx, *jobOpts)
	} else {
		if jobOpts.SourceConfig != nil {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("revai: source_config requires URI audio")}
		}
		audio, audioErr := req.Audio.Bytes()
		if audioErr != nil {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: audioErr}
		}
		submitted, err = t.api.upload(ctx, audio, req.Audio.MIME, *jobOpts)
	}
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	final, err := t.pollUntilDone(ctx, submitted.ID)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	if final.Status == jobStatusFailed {
		return nil, &invocation.Failure{Kind: invocation.FailureFailed, Cause: fmt.Errorf("revai: transcription failed: %s", final.FailureReason)}
	}
	text, err := t.api.getTranscriptText(ctx, final.ID)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	return newResponse(final, text, jobOpts.Transcriber)
}

// buildJobOptions resolves options and admits the native extension without
// Core-owned fields. source_config may carry only authorization headers,
// because Audio.Source owns the media URL.
func (t *TranscriptionModel) buildJobOptions(req *transcription.Request) (*jobOptions, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	options, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	nativeFields, _, err := options.Extensions.Decode[map[string]json.RawMessage](RequestExtensionKey)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{fieldMediaURL, fieldLanguage, fieldTranscriber} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("revai: extension %q field %q is owned by Core", RequestExtensionKey, field)
		}
	}
	if raw, exists := nativeFields[fieldSourceConfig]; exists {
		var sourceFields map[string]json.RawMessage
		if err = jsonv2.Unmarshal(raw, &sourceFields); err != nil {
			return nil, fmt.Errorf("revai: source_config: %w", err)
		}
		for field := range sourceFields {
			if field != fieldAuthHeaders {
				return nil, fmt.Errorf("revai: source_config field %q is not a native authorization option; Audio.Source owns the URL", field)
			}
		}
	}
	jobOpts, _, err := options.Extensions.Decode[jobOptions](RequestExtensionKey)
	if err != nil {
		return nil, err
	}
	jobOpts.Language = options.Language
	jobOpts.Transcriber = options.Model
	if jobOpts.Transcriber != ModelMachine && jobOpts.Transcriber != ModelHuman {
		return nil, fmt.Errorf("revai: transcription model must be %q or %q, got %q", ModelMachine, ModelHuman, jobOpts.Transcriber)
	}
	return &jobOpts, nil
}

func newResponse(final *job, text, transcriber string) (*transcription.Response, error) {
	var outputMetadata metadata.Map
	if final.Language != "" {
		if err := outputMetadata.Set(metadataLanguage, final.Language); err != nil {
			return nil, err
		}
	}
	if final.DurationSeconds > 0 {
		if err := outputMetadata.Set(metadataDurationSeconds, final.DurationSeconds); err != nil {
			return nil, err
		}
	}
	output, err := transcription.NewOutput(text, outputMetadata)
	if err != nil {
		return nil, err
	}
	meta := &transcription.ResponseMetadata{Model: transcriber}
	if err := meta.Extra.Set(metadataJobID, final.ID); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(ResponseExtensionKey, final); err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, meta)
}

func (t *TranscriptionModel) pollUntilDone(ctx context.Context, id string) (*job, error) {
	return t.polling.Poll(ctx, func(ctx context.Context) (*job, bool, error) {
		resp, err := t.api.getJob(ctx, id)
		if err != nil {
			return nil, false, err
		}
		switch resp.Status {
		case jobStatusTranscribed, jobStatusFailed:
			return resp, true, nil
		case jobStatusInProgress:
			return resp, false, nil
		default:
			return nil, false, fmt.Errorf("revai: job %s reports unrecognized status %q", id, resp.Status)
		}
	})
}
