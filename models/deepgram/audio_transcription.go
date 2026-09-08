package deepgram

import (
	"cmp"
	"context"
	"errors"
	"net/http"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
)

// AudioTranscriptionModelConfig binds provider access and defaults shared by every transcription call.
type AudioTranscriptionModelConfig struct {
	APIKey           string
	DefaultOptions   transcription.Options
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (a AudioTranscriptionModelConfig) Validate() error {
	if a.APIKey == "" {
		return errors.New("deepgram: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("deepgram: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	if a.MaxResponseBytes < 0 {
		return errors.New("deepgram: MaxResponseBytes must not be negative")
	}
	return nil
}

var _ transcription.Model = (*AudioTranscriptionModel)(nil)

// AudioTranscriptionModel wraps Deepgram's /v1/listen synchronous
// transcription endpoint. Supported models include "nova-3" (latest),
// "nova-2", "enhanced", "base". Diarization, smart_format, punctuation
// and the long tail of Deepgram knobs live on [ListenParams] and reach
// the API via the extension-threaded SDK params, see [getOptionsParams].
//
// The returned [transcription.Output] holds the merged transcript of
// channel 0 / alternative 0; per-word + per-utterance breakdown is
// stashed on the output metadata so callers needing diarization or
// timestamps can dig in.
type AudioTranscriptionModel struct {
	api            *api
	defaultOptions transcription.Options
}

// NewAudioTranscriptionModel rejects an invalid provider binding before the first transcription call.
func NewAudioTranscriptionModel(_ context.Context, config AudioTranscriptionModelConfig) (*AudioTranscriptionModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(apiConfig{
		APIKey:           config.APIKey,
		BaseURL:          config.BaseURL,
		HTTPClient:       config.HTTPClient,
		MaxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes),
	})
	if err != nil {
		return nil, err
	}

	return &AudioTranscriptionModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
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
	paramsValue, _, err := effectiveOptions.Extensions.Decode[listenParams](TranscriptionRequestExtensionKey)
	params := &paramsValue
	if err != nil {
		return nil, err
	}
	if params.Model == "" {
		params.Model = effectiveOptions.Model
	}
	if params.Language == "" && effectiveOptions.Language != "" {
		params.Language = effectiveOptions.Language
	}
	if params.Summarize == "v1" {
		return nil, errors.New("deepgram: summarize=v1 is deprecated; use true or v2")
	}

	audio, err := req.Audio.Bytes()
	if err != nil {
		return nil, err
	}

	contentType := req.Audio.MIME

	apiResp, err := a.api.listen(ctx, audio, contentType, params)
	if err != nil {
		return nil, err
	}

	if len(apiResp.Results.Channels) == 0 || len(apiResp.Results.Channels[0].Alternatives) == 0 {
		return nil, errors.New("deepgram: response has no transcript alternatives")
	}

	alt := apiResp.Results.Channels[0].Alternatives[0]

	var outputMetadata metadata.Map
	if setErr := outputMetadata.Set("deepgram/confidence", alt.Confidence); setErr != nil {
		return nil, setErr
	}
	if setErr := outputMetadata.Set("deepgram/words", alt.Words); setErr != nil {
		return nil, setErr
	}
	if len(apiResp.Results.Utterances) > 0 {
		if setErr := outputMetadata.Set("deepgram/utterances", apiResp.Results.Utterances); setErr != nil {
			return nil, setErr
		}
	}

	output, err := transcription.NewOutput(alt.Transcript, outputMetadata)
	if err != nil {
		return nil, err
	}

	meta := &transcription.ResponseMetadata{Model: params.Model}
	if err := meta.Extra.Set("deepgram/request_id", apiResp.Metadata.RequestID); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set("deepgram/duration_seconds", apiResp.Metadata.Duration); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set("deepgram/channels", apiResp.Metadata.Channels); err != nil {
		return nil, err
	}
	if err := meta.Extra.Set(TranscriptionResponseExtensionKey, apiResp.Raw); err != nil {
		return nil, err
	}

	return transcription.NewResponse(output, meta)
}
