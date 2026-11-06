package gladia

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

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
	PollInterval   time.Duration
	PollTimeout    time.Duration
}

func (t TranscriptionModelConfig) Validate() error {
	if t.APIKey == "" {
		return errors.New("gladia: APIKey is required")
	}
	if t.DefaultOptions.Model == "" {
		return errors.New("gladia: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	if t.PollInterval < 0 {
		return errors.New("gladia: PollInterval must not be negative")
	}
	if t.PollTimeout < 0 {
		return errors.New("gladia: PollTimeout must not be negative")
	}
	return nil
}

var _ transcription.Model = (*TranscriptionModel)(nil)

// TranscriptionModel wraps Gladia's async transcription flow.
// One Call uploads → creates job → polls until "done". Diarization /
// translation / summarization / NER / subtitles all reach the wire via
// official JSON option names under RequestExtensionKey.
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
	pollInterval := config.PollInterval
	if pollInterval == 0 {
		pollInterval = DefaultPollInterval
	}
	pollTimeout := config.PollTimeout
	if pollTimeout == 0 {
		pollTimeout = DefaultPollTimeout
	}
	return &TranscriptionModel{api: api, defaultOptions: config.DefaultOptions.Clone(), pollInterval: pollInterval, pollTimeout: pollTimeout}, nil
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	effectiveOptions, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	nativeFields, _, err := effectiveOptions.Extensions.Decode[map[string]any](RequestExtensionKey)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	for _, field := range []string{fieldAudioURL, fieldModel} {
		if _, exists := nativeFields[field]; exists {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: fmt.Errorf("gladia: extension %q field %q is owned by Core", RequestExtensionKey, field)}
		}
	}
	if language, ok := nativeFields[fieldLanguageConfig].(map[string]any); ok {
		if _, exists := language[fieldLanguages]; exists {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: fmt.Errorf("gladia: extension %q language_config.languages is owned by Core", RequestExtensionKey)}
		}
	}
	apiReqValue, _, err := effectiveOptions.Extensions.Decode[transcriptionRequest](RequestExtensionKey)
	apiReq := &apiReqValue
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	apiReq.Model = effectiveOptions.Model
	if effectiveOptions.Language != "" {
		if apiReq.LanguageConfig == nil {
			apiReq.LanguageConfig = &languageConfig{}
		}
		apiReq.LanguageConfig.Languages = []string{effectiveOptions.Language}
	}
	if validateTranscriptionRequestErr := apiReq.validate(); validateTranscriptionRequestErr != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: validateTranscriptionRequestErr}
	}
	if req.Audio.Source.Kind == media.SourceURI {
		apiReq.AudioURL = req.Audio.Source.URI
	} else {
		var audio []byte
		audio, err = req.Audio.Bytes()
		if err != nil {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
		}
		var uploaded *uploadResponse
		uploaded, err = t.api.upload(ctx, audio, req.Audio.MIME)
		if err != nil {
			return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
		}
		apiReq.AudioURL = uploaded.AudioURL
	}

	job, err := t.api.createTranscription(ctx, apiReq)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}

	final, err := t.pollUntilDone(ctx, job.ID)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	if final.Status == transcriptionStatusError {
		return nil, &invocation.Failure{Kind: invocation.FailureFailed, Cause: final.failure()}
	}

	var outputMetadata metadata.Map
	if len(final.Result.Transcription.Languages) > 0 {
		if setErr := outputMetadata.Set(metadataLanguages, final.Result.Transcription.Languages); setErr != nil {
			return nil, setErr
		}
	}
	if len(final.Result.Transcription.Utterances) > 0 {
		if setErr := outputMetadata.Set(metadataUtterances, final.Result.Transcription.Utterances); setErr != nil {
			return nil, setErr
		}
	}
	if final.Result.Translation != nil {
		if setErr := outputMetadata.Set(metadataTranslation, final.Result.Translation); setErr != nil {
			return nil, setErr
		}
	}
	if final.Result.Summarization != nil {
		if setErr := outputMetadata.Set(metadataSummarization, final.Result.Summarization); setErr != nil {
			return nil, setErr
		}
	}

	if final.Result.Transcription.FullTranscript == nil {
		return nil, fmt.Errorf("%w: gladia: transcription requires full_transcript", transcription.ErrInvalidResponse)
	}
	output, err := transcription.NewOutput(*final.Result.Transcription.FullTranscript, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &transcription.ResponseMetadata{Model: apiReq.Model}
	if err := meta.Extra.Set(metadataTranscriptID, final.ID); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(ResponseExtensionKey, final.Raw); err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, meta)
}

func (t *TranscriptionModel) pollUntilDone(ctx context.Context, id string) (*transcriptionResult, error) {
	deadline, cancel := context.WithTimeout(ctx, t.pollTimeout)
	defer cancel()
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()
	for {
		resp, err := t.api.getTranscription(deadline, id)
		if err != nil {
			return nil, err
		}
		switch resp.Status {
		case transcriptionStatusDone, transcriptionStatusError:
			return resp, nil
		case transcriptionStatusQueued, transcriptionStatusProcessing:
		default:
			return nil, fmt.Errorf("gladia: transcription %s reports unrecognized status %q", id, resp.Status)
		}
		select {
		case <-deadline.Done():
			return nil, deadline.Err()
		case <-ticker.C:
		}
	}
}
