package assemblyai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

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

	// Polling bounds the wait for AssemblyAI's async job; zero fields
	// select DefaultPollInterval and DefaultPollTimeout.
	Polling invocation.Polling
}

func (t TranscriptionModelConfig) apiConfig() apiConfig {
	return apiConfig{
		APIKey:     t.APIKey,
		BaseURL:    t.BaseURL,
		HTTPClient: t.HTTPClient,
	}
}

func (t TranscriptionModelConfig) Validate() error {
	if err := t.apiConfig().validate(); err != nil {
		return err
	}
	if t.DefaultOptions.Model == "" {
		return errors.New("assemblyai: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	if err := t.Polling.Validate(); err != nil {
		return fmt.Errorf("assemblyai: Polling: %w", err)
	}
	return nil
}

var _ transcription.Model = (*TranscriptionModel)(nil)

// TranscriptionModel wraps AssemblyAI's async transcription flow
// behind a synchronous [transcription.Model.Call] surface. One Call
// uploads the audio, enqueues a job, and polls until the job reaches a
// terminal state — callers don't see the polling unless their ctx
// cancels or the polling timeout elapses.
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

	return &TranscriptionModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
		polling:        config.Polling.Or(invocation.Polling{Interval: DefaultPollInterval, Timeout: DefaultPollTimeout}),
	}, nil
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	effectiveOptions, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	nativeFields, _, decodeErr := effectiveOptions.Extensions.Decode[map[string]any](RequestExtensionKey)
	if decodeErr != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: decodeErr}
	}
	for _, field := range []string{fieldAudioURL, fieldLanguageCode} {
		if _, exists := nativeFields[field]; exists {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: fmt.Errorf("assemblyai: extension %q field %q is owned by Core", RequestExtensionKey, field)}
		}
	}

	apiReqValue, _, err := effectiveOptions.Extensions.Decode[transcriptRequest](RequestExtensionKey)
	apiReq := &apiReqValue
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	language, err := newLanguageSelection(effectiveOptions.Language, apiReq.LanguageCodes, apiReq.LanguageDetection)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	language.apply(apiReq)
	apiReq.SpeechModels = prioritizedSpeechModels(effectiveOptions.Model, apiReq.SpeechModels)
	if validateTranscriptRequestErr := apiReq.validate(); validateTranscriptRequestErr != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: validateTranscriptRequestErr}
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
		uploaded, err = t.api.upload(ctx, audio)
		if err != nil {
			return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
		}
		apiReq.AudioURL = uploaded.UploadURL
	}

	job, err := t.api.createTranscript(ctx, apiReq)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}

	final, err := t.pollUntilDone(ctx, job.ID)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	if final.Status == statusErrored {
		return nil, &invocation.Failure{Kind: invocation.FailureFailed, Cause: fmt.Errorf("assemblyai: transcription failed: %s", final.Error)}
	}

	return t.buildResponse(final)
}

func (t *TranscriptionModel) pollUntilDone(ctx context.Context, id string) (*transcriptResponse, error) {
	return t.polling.Poll(ctx, func(ctx context.Context) (*transcriptResponse, bool, error) {
		resp, err := t.api.get(ctx, id)
		if err != nil {
			return nil, false, err
		}
		switch resp.Status {
		case statusCompleted, statusErrored:
			return resp, true, nil
		case statusQueued, statusProcessing:
			return resp, false, nil
		default:
			return nil, false, fmt.Errorf("assemblyai: transcript %s reports unrecognized status %q", id, resp.Status)
		}
	})
}

func (t *TranscriptionModel) buildResponse(apiResp *transcriptResponse) (*transcription.Response, error) {
	var outputMetadata metadata.Map
	if apiResp.Confidence != nil {
		if err := outputMetadata.Set(metadataConfidence, *apiResp.Confidence); err != nil {
			return nil, err
		}
	}
	if apiResp.LanguageCode != "" {
		if err := outputMetadata.Set(metadataLanguageCode, apiResp.LanguageCode); err != nil {
			return nil, err
		}
	}
	if len(apiResp.Utterances) > 0 {
		if err := outputMetadata.Set(metadataUtterances, apiResp.Utterances); err != nil {
			return nil, err
		}
	}
	if len(apiResp.Words) > 0 {
		if err := outputMetadata.Set(metadataWords, apiResp.Words); err != nil {
			return nil, err
		}
	}

	if apiResp.Text == nil {
		return nil, fmt.Errorf("%w: assemblyai: transcription requires text", transcription.ErrInvalidResponse)
	}
	output, err := transcription.NewOutput(*apiResp.Text, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &transcription.ResponseMetadata{Model: apiResp.SpeechModelUsed}
	if err := meta.Extra.Set(metadataTranscriptID, apiResp.ID); err != nil {
		return nil, err
	}
	if apiResp.AudioDuration != nil {
		if err := meta.Extra.Set(metadataDurationSeconds, *apiResp.AudioDuration); err != nil {
			return nil, err
		}
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
