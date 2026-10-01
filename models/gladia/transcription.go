package gladia

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
)

// TranscriptionModelConfig binds provider access and defaults shared by every transcription call.
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

// NewTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewTranscriptionModel(_ context.Context, config TranscriptionModelConfig) (*TranscriptionModel, error) {
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
	nativeFields, _, err := effectiveOptions.Extensions.Decode[map[string]any](RequestExtensionKey)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{"audio_url", "model"} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("gladia: extension %q field %q is owned by Core", RequestExtensionKey, field)
		}
	}
	if language, ok := nativeFields["language_config"].(map[string]any); ok {
		if _, exists := language["languages"]; exists {
			return nil, fmt.Errorf("gladia: extension %q language_config.languages is owned by Core", RequestExtensionKey)
		}
	}
	apiReqValue, _, err := effectiveOptions.Extensions.Decode[transcriptionRequest](RequestExtensionKey)
	apiReq := &apiReqValue
	if err != nil {
		return nil, err
	}
	apiReq.Model = effectiveOptions.Model
	if effectiveOptions.Language != "" {
		if apiReq.LanguageConfig == nil {
			apiReq.LanguageConfig = &languageConfig{}
		}
		apiReq.LanguageConfig.Languages = []string{effectiveOptions.Language}
	}
	if validateTranscriptionRequestErr := apiReq.validate(); validateTranscriptionRequestErr != nil {
		return nil, validateTranscriptionRequestErr
	}
	if req.Audio.Source.Kind == media.SourceURI {
		apiReq.AudioURL = req.Audio.Source.URI
	} else {
		var audio []byte
		audio, err = req.Audio.Bytes()
		if err != nil {
			return nil, err
		}
		var uploaded *uploadResponse
		uploaded, err = t.api.upload(ctx, audio, req.Audio.MIME)
		if err != nil {
			return nil, err
		}
		apiReq.AudioURL = uploaded.AudioURL
	}

	job, err := t.api.createTranscription(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	final, err := t.pollUntilDone(ctx, job.ID)
	if err != nil {
		return nil, err
	}

	var outputMetadata metadata.Map
	if len(final.Result.Transcription.Languages) > 0 {
		if setErr := outputMetadata.Set("gladia/languages", final.Result.Transcription.Languages); setErr != nil {
			return nil, setErr
		}
	}
	if len(final.Result.Transcription.Utterances) > 0 {
		if setErr := outputMetadata.Set("gladia/utterances", final.Result.Transcription.Utterances); setErr != nil {
			return nil, setErr
		}
	}
	if final.Result.Translation != nil {
		if setErr := outputMetadata.Set("gladia/translation", final.Result.Translation); setErr != nil {
			return nil, setErr
		}
	}
	if final.Result.Summarization != nil {
		if setErr := outputMetadata.Set("gladia/summarization", final.Result.Summarization); setErr != nil {
			return nil, setErr
		}
	}

	output, err := transcription.NewOutput(final.Result.Transcription.FullTranscript, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &transcription.ResponseMetadata{Model: apiReq.Model}
	if err := meta.Extra.Set("gladia/transcript_id", final.ID); err != nil {
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
		case transcriptionStatusDone:
			return resp, nil
		case transcriptionStatusQueued, transcriptionStatusProcessing:
			// The two states Gladia documents as still moving.
		case transcriptionStatusError:
			return nil, fmt.Errorf("gladia: transcription failed: %s", resp.ErrorCode)
		default:
			// Continuing to poll is only safe for a state known to advance, so
			// an unrecognized one is reported rather than absorbed: an added
			// end state would otherwise arrive as this call's own timeout,
			// whose obvious remedies are both wrong.
			return nil, fmt.Errorf("gladia: transcription %s reports unrecognized status %q", id, resp.Status)
		}
		select {
		case <-deadline.Done():
			return nil, deadline.Err()
		case <-ticker.C:
		}
	}
}
