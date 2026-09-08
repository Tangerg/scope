package assemblyai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
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

	// PollInterval / PollTimeout configure the synchronous Call
	// wrapper around AssemblyAI's async job model. Zero values fall
	// back to [DefaultPollInterval] / [DefaultPollTimeout].
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func (a AudioTranscriptionModelConfig) Validate() error {
	if a.APIKey == "" {
		return errors.New("assemblyai: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("assemblyai: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	if a.PollInterval < 0 {
		return errors.New("assemblyai: PollInterval must not be negative")
	}
	if a.PollTimeout < 0 {
		return errors.New("assemblyai: PollTimeout must not be negative")
	}
	return nil
}

var _ transcription.Model = (*AudioTranscriptionModel)(nil)

// AudioTranscriptionModel wraps AssemblyAI's async transcription flow
// behind a synchronous [transcription.Model.Call] surface. One Call
// uploads the audio, enqueues a job, and polls until the job reaches a
// terminal state — callers don't see the polling unless their ctx
// cancels or [PollTimeout] elapses.
//
// Speaker labels, sentiment analysis, auto chapters, entity detection
// and the rest of AssemblyAI's analysis features live on
// [TranscriptRequest] and reach the API via the extension-threaded SDK
// params, see [getOptionsParams].
//
// Audio source: the [transcription.Request].Audio is uploaded by
// bytes; if the audio is large and already hosted somewhere the API
// can reach, callers can override the audio_url by setting it on the
// extension-threaded TranscriptRequest and the model will skip the
// /upload roundtrip.
type AudioTranscriptionModel struct {
	api            *api
	defaultOptions transcription.Options
	pollInterval   time.Duration
	pollTimeout    time.Duration
}

// NewAudioTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewAudioTranscriptionModel(_ context.Context, config AudioTranscriptionModelConfig) (*AudioTranscriptionModel, error) {
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

	return &AudioTranscriptionModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
		pollInterval:   pollInterval,
		pollTimeout:    pollTimeout,
	}, nil
}

func (a *AudioTranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	apiReqValue, _, err := effectiveOptions.Extensions.Decode[transcriptRequest](RequestExtensionKey)
	apiReq := &apiReqValue
	if err != nil {
		return nil, err
	}
	apiReq.SpeechModels = prioritizedSpeechModels(effectiveOptions.Model, apiReq.SpeechModels)
	if validateTranscriptRequestErr := validateTranscriptRequest(apiReq); validateTranscriptRequestErr != nil {
		return nil, validateTranscriptRequestErr
	}
	if apiReq.LanguageCode == "" && effectiveOptions.Language != "" {
		apiReq.LanguageCode = effectiveOptions.Language
	}

	// Skip the /upload roundtrip when the caller already gave us a
	// reachable URL via Extra; otherwise upload the bytes.
	if apiReq.AudioURL == "" {
		var audio []byte
		audio, err = req.Audio.Bytes()
		if err != nil {
			return nil, err
		}
		var uploaded *uploadResponse
		uploaded, err = a.api.upload(ctx, audio)
		if err != nil {
			return nil, err
		}
		apiReq.AudioURL = uploaded.UploadURL
	}

	job, err := a.api.createTranscript(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	final, err := a.pollUntilDone(ctx, job.ID)
	if err != nil {
		return nil, err
	}

	return a.buildResponse(final)
}

// pollUntilDone re-fetches the transcript every [pollInterval] until
// it reaches "completed" / "error", or the ctx / pollTimeout deadline
// fires.
func (a *AudioTranscriptionModel) pollUntilDone(ctx context.Context, id string) (*transcriptResponse, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, a.pollTimeout)
	defer cancel()

	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()

	// First fetch immediately rather than waiting one tick — short
	// audio often finishes before our first poll.
	for {
		resp, err := a.api.get(deadlineCtx, id)
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

func (a *AudioTranscriptionModel) buildResponse(apiResp *transcriptResponse) (*transcription.Response, error) {
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

func validateTranscriptRequest(req *transcriptRequest) error {
	for index, model := range req.SpeechModels {
		if model != ModelUniversal3Point5Pro && model != ModelUniversal2 {
			return fmt.Errorf("assemblyai: speech_models[%d] must be %q or %q, got %q", index, ModelUniversal3Point5Pro, ModelUniversal2, model)
		}
	}
	if req.Prompt != "" && len(req.KeytermsPrompt) > 0 {
		return errors.New("assemblyai: prompt and keyterms_prompt are mutually exclusive")
	}
	if req.LanguageConfidenceThreshold != nil && (*req.LanguageConfidenceThreshold < 0 || *req.LanguageConfidenceThreshold > 1) {
		return fmt.Errorf("assemblyai: language_confidence_threshold must be between 0 and 1, got %g", *req.LanguageConfidenceThreshold)
	}
	if req.SpeechThreshold != nil && (*req.SpeechThreshold < 0 || *req.SpeechThreshold > 1) {
		return fmt.Errorf("assemblyai: speech_threshold must be between 0 and 1, got %g", *req.SpeechThreshold)
	}
	if req.ContentSafetyConfidence != nil && (*req.ContentSafetyConfidence < 25 || *req.ContentSafetyConfidence > 100) {
		return fmt.Errorf("assemblyai: content_safety_confidence must be between 25 and 100, got %d", *req.ContentSafetyConfidence)
	}
	return nil
}
