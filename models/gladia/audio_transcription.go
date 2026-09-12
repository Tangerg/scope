package gladia

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
		return errors.New("gladia: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("gladia: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	if a.PollInterval < 0 {
		return errors.New("gladia: PollInterval must not be negative")
	}
	if a.PollTimeout < 0 {
		return errors.New("gladia: PollTimeout must not be negative")
	}
	return nil
}

var _ transcription.Model = (*AudioTranscriptionModel)(nil)

// AudioTranscriptionModel wraps Gladia's async transcription flow.
// One Call uploads → creates job → polls until "done". Diarization /
// translation / summarization / NER / subtitles all reach the wire via
// the extension-threaded [TranscriptionRequest].
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
	if apiReq.AudioURL == "" {
		var audio []byte
		audio, err = req.Audio.Bytes()
		if err != nil {
			return nil, err
		}
		var uploaded *uploadResponse
		uploaded, err = a.api.upload(ctx, audio, req.Audio.MIME)
		if err != nil {
			return nil, err
		}
		apiReq.AudioURL = uploaded.AudioURL
	}

	job, err := a.api.createTranscription(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	final, err := a.pollUntilDone(ctx, job.ID)
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

func (a *AudioTranscriptionModel) pollUntilDone(ctx context.Context, id string) (*transcriptionResult, error) {
	deadline, cancel := context.WithTimeout(ctx, a.pollTimeout)
	defer cancel()
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		resp, err := a.api.getTranscription(deadline, id)
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
