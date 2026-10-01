package assemblyai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
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

	// PollInterval / PollTimeout configure the synchronous Call
	// wrapper around AssemblyAI's async job model. Zero values fall
	// back to [DefaultPollInterval] / [DefaultPollTimeout].
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func (t TranscriptionModelConfig) Validate() error {
	if t.APIKey == "" {
		return errors.New("assemblyai: APIKey is required")
	}
	if t.DefaultOptions.Model == "" {
		return errors.New("assemblyai: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	if t.PollInterval < 0 {
		return errors.New("assemblyai: PollInterval must not be negative")
	}
	if t.PollTimeout < 0 {
		return errors.New("assemblyai: PollTimeout must not be negative")
	}
	return nil
}

var _ transcription.Model = (*TranscriptionModel)(nil)

// TranscriptionModel wraps AssemblyAI's async transcription flow
// behind a synchronous [transcription.Model.Call] surface. One Call
// uploads the audio, enqueues a job, and polls until the job reaches a
// terminal state — callers don't see the polling unless their ctx
// cancels or [PollTimeout] elapses.
//
// Speaker labels, sentiment analysis, auto chapters, entity detection
// and the rest of AssemblyAI's analysis features live on
// official JSON option names under RequestExtensionKey.
//
// Audio comes only from transcription.Request.Audio. URI media is submitted
// directly; inline bytes are uploaded before the transcription is created.
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

	api, err := newAPI(apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
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

	return &TranscriptionModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
		pollInterval:   pollInterval,
		pollTimeout:    pollTimeout,
	}, nil
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	effectiveOptions, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	nativeFields, _, decodeErr := effectiveOptions.Extensions.Decode[map[string]any](RequestExtensionKey)
	if decodeErr != nil {
		return nil, decodeErr
	}
	for _, field := range []string{"audio_url", "language_code"} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("assemblyai: extension %q field %q is owned by Core", RequestExtensionKey, field)
		}
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[transcriptRequest](RequestExtensionKey)
	apiReq := &apiReqValue
	if err != nil {
		return nil, err
	}
	apiReq.SpeechModels = prioritizedSpeechModels(effectiveOptions.Model, apiReq.SpeechModels)
	if validateTranscriptRequestErr := apiReq.validate(); validateTranscriptRequestErr != nil {
		return nil, validateTranscriptRequestErr
	}
	apiReq.LanguageCode = effectiveOptions.Language

	if req.Audio.Source.Kind == media.SourceURI {
		apiReq.AudioURL = req.Audio.Source.URI
	} else {
		var audio []byte
		audio, err = req.Audio.Bytes()
		if err != nil {
			return nil, err
		}
		var uploaded *uploadResponse
		uploaded, err = t.api.upload(ctx, audio)
		if err != nil {
			return nil, err
		}
		apiReq.AudioURL = uploaded.UploadURL
	}

	job, err := t.api.createTranscript(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	final, err := t.pollUntilDone(ctx, job.ID)
	if err != nil {
		return nil, err
	}

	return t.buildResponse(final)
}

// pollUntilDone re-fetches the transcript every [pollInterval] until
// it reaches "completed" / "error", or the ctx / pollTimeout deadline
// fires.
func (t *TranscriptionModel) pollUntilDone(ctx context.Context, id string) (*transcriptResponse, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, t.pollTimeout)
	defer cancel()

	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	// First fetch immediately rather than waiting one tick — short
	// audio often finishes before our first poll.
	for {
		resp, err := t.api.get(deadlineCtx, id)
		if err != nil {
			return nil, err
		}
		switch resp.Status {
		case statusCompleted:
			return resp, nil
		case statusQueued, statusProcessing:
			// The two states AssemblyAI documents as still moving.
		case statusErrored:
			return nil, fmt.Errorf("assemblyai: transcription failed: %s", resp.Error)
		default:
			// Continuing to poll is only safe for a state known to advance, so
			// an unrecognized one is reported rather than absorbed: an added
			// end state would otherwise arrive as this call's own timeout,
			// whose obvious remedies are both wrong.
			return nil, fmt.Errorf("assemblyai: transcript %s reports unrecognized status %q", id, resp.Status)
		}

		select {
		case <-deadlineCtx.Done():
			return nil, deadlineCtx.Err()
		case <-ticker.C:
		}
	}
}

func (t *TranscriptionModel) buildResponse(apiResp *transcriptResponse) (*transcription.Response, error) {
	var outputMetadata metadata.Map
	if err := outputMetadata.Set("assemblyai/confidence", apiResp.Confidence); err != nil {
		return nil, err
	}
	if apiResp.LanguageCode != "" {
		if err := outputMetadata.Set("assemblyai/language_code", apiResp.LanguageCode); err != nil {
			return nil, err
		}
	}
	if len(apiResp.Utterances) > 0 {
		if err := outputMetadata.Set("assemblyai/utterances", apiResp.Utterances); err != nil {
			return nil, err
		}
	}
	if len(apiResp.Words) > 0 {
		if err := outputMetadata.Set("assemblyai/words", apiResp.Words); err != nil {
			return nil, err
		}
	}

	output, err := transcription.NewOutput(apiResp.Text, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &transcription.ResponseMetadata{Model: apiResp.SpeechModelUsed}
	if err := meta.Extra.Set("assemblyai/transcript_id", apiResp.ID); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set("assemblyai/audio_duration_seconds", apiResp.AudioDuration); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(ResponseExtensionKey, apiResp.Raw); err != nil {
		return nil, err
	}

	return transcription.NewResponse(output, meta)
}

func prioritizedSpeechModels(primary string, fallbacks []string) []string {
	models := make([]string, 0, len(fallbacks)+1)
	models = append(models, primary)
	for _, model := range fallbacks {
		if !slices.Contains(models, model) {
			models = append(models, model)
		}
	}
	return models
}
