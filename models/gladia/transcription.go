package gladia

import (
	"context"
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
	// Polling bounds the wait for the async Gladia transcription; zero fields
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
		return errors.New("gladia: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	if err := t.Polling.Validate(); err != nil {
		return fmt.Errorf("gladia: Polling: %w", err)
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
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	apiReq, err := t.buildRequest(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	if req.Audio.Source.Kind == media.SourceURI {
		apiReq.AudioURL = req.Audio.Source.URI
	} else {
		audio, audioErr := req.Audio.Bytes()
		if audioErr != nil {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: audioErr}
		}
		uploaded, uploadErr := t.api.upload(ctx, audio, req.Audio.MIME)
		if uploadErr != nil {
			return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: uploadErr}
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
	return newResponse(final, apiReq.Model)
}

// buildRequest resolves a validated request's options and admits the native extension without
// Core-owned fields; the audio URL is set once the audio is reachable.
func (t *TranscriptionModel) buildRequest(req *transcription.Request) (*transcriptionRequest, error) {
	options, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	nativeFields, _, err := options.Extensions.Decode[map[string]any](RequestExtensionKey)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{fieldAudioURL, fieldModel} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("gladia: extension %q field %q: %w", RequestExtensionKey, field, metadata.ErrCoreOwnedField)
		}
	}
	if language, ok := nativeFields[fieldLanguageConfig].(map[string]any); ok {
		if _, exists := language[fieldLanguages]; exists {
			return nil, fmt.Errorf("gladia: extension %q language_config.languages: %w", RequestExtensionKey, metadata.ErrCoreOwnedField)
		}
	}
	apiReq, _, err := options.Extensions.Decode[transcriptionRequest](RequestExtensionKey)
	if err != nil {
		return nil, err
	}
	apiReq.Model = options.Model
	if options.Language != "" {
		if apiReq.LanguageConfig == nil {
			apiReq.LanguageConfig = &languageConfig{}
		}
		apiReq.LanguageConfig.Languages = []string{options.Language}
	}
	if err = apiReq.validate(); err != nil {
		return nil, err
	}
	return &apiReq, nil
}

// newResponse projects a finished job: the full transcript is the output, and
// languages, utterances, translation and summarization become its metadata.
func newResponse(final *transcriptionResult, model string) (*transcription.Response, error) {
	if final.Result.Transcription.FullTranscript == nil {
		return nil, fmt.Errorf("%w: gladia: transcription requires full_transcript", transcription.ErrInvalidResponse)
	}
	var outputMetadata metadata.Map
	if languages := final.Result.Transcription.Languages; len(languages) > 0 {
		if err := outputMetadata.Set(metadataLanguages, languages); err != nil {
			return nil, err
		}
	}
	if utterances := final.Result.Transcription.Utterances; len(utterances) > 0 {
		if err := outputMetadata.Set(metadataUtterances, utterances); err != nil {
			return nil, err
		}
	}
	if final.Result.Translation != nil {
		if err := outputMetadata.Set(metadataTranslation, final.Result.Translation); err != nil {
			return nil, err
		}
	}
	if final.Result.Summarization != nil {
		if err := outputMetadata.Set(metadataSummarization, final.Result.Summarization); err != nil {
			return nil, err
		}
	}
	output, err := transcription.NewOutput(*final.Result.Transcription.FullTranscript, outputMetadata)
	if err != nil {
		return nil, err
	}
	meta := &transcription.ResponseMetadata{Model: model}
	if err := meta.Extra.Set(metadataTranscriptID, final.ID); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(ResponseExtensionKey, final.Raw); err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, meta)
}

func (t *TranscriptionModel) pollUntilDone(ctx context.Context, id string) (*transcriptionResult, error) {
	return t.polling.Poll(ctx, func(ctx context.Context) (*transcriptionResult, bool, error) {
		resp, err := t.api.getTranscription(ctx, id)
		if err != nil {
			return nil, false, err
		}
		switch resp.Status {
		case transcriptionStatusDone, transcriptionStatusError:
			return resp, true, nil
		case transcriptionStatusQueued, transcriptionStatusProcessing:
			return resp, false, nil
		default:
			return nil, false, fmt.Errorf("gladia: transcription %s reports unrecognized status %q", id, resp.Status)
		}
	})
}
