package deepgram

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/transcription"
)

type TranscriptionModelConfig struct {
	APIKey           string
	DefaultOptions   transcription.Options
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (t TranscriptionModelConfig) apiConfig() apiConfig {
	return apiConfig{
		APIKey:           t.APIKey,
		BaseURL:          t.BaseURL,
		HTTPClient:       t.HTTPClient,
		MaxResponseBytes: t.MaxResponseBytes,
	}
}

func (t TranscriptionModelConfig) Validate() error {
	if err := t.apiConfig().validate(); err != nil {
		return err
	}
	if t.DefaultOptions.Model == "" {
		return errors.New("deepgram: DefaultOptions.Model is required")
	}
	if err := t.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ transcription.Model = (*TranscriptionModel)(nil)

// TranscriptionModel wraps Deepgram's /v1/listen synchronous
// transcription endpoint. Supported models include "nova-3" (latest),
// "nova-2", "enhanced", "base". Diarization, smart_format, punctuation
// and provider query options use official JSON keys under
// TranscriptionRequestExtensionKey. Core owns model and language.
//
// The returned [transcription.Output] holds the merged transcript of
// channel 0 / alternative 0; per-word + per-utterance breakdown is
// stashed on the output metadata so callers needing diarization or
// timestamps can dig in.
type TranscriptionModel struct {
	api            *api
	defaultOptions transcription.Options
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
	}, nil
}

func (t *TranscriptionModel) Call(ctx context.Context, req *transcription.Request) (*transcription.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	params, err := t.buildParams(req)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	audio, err := req.Audio.Bytes()
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
	}
	apiResp, err := t.api.listen(ctx, audio, req.Audio.MIME, params)
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	return newTranscriptionResponse(apiResp, params.Model)
}

// buildParams resolves a validated request's options and admits the native extension: Core owns
// model and language, and a field with a dedicated parameter may not also
// arrive through Extra.
func (t *TranscriptionModel) buildParams(req *transcription.Request) (*listenParams, error) {
	options, err := t.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}
	nativeFields, _, err := options.Extensions.Decode[map[string]any](TranscriptionRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{fieldModel, fieldLanguage} {
		if _, exists := nativeFields[field]; exists {
			return nil, fmt.Errorf("deepgram: extension %q field %q is owned by Core", TranscriptionRequestExtensionKey, field)
		}
	}
	params, _, err := options.Extensions.Decode[listenParams](TranscriptionRequestExtensionKey)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{fieldModel, fieldLanguage, fieldTier, fieldVersion, fieldPunctuate, fieldSmartFormat, fieldDiarize, fieldNumerals, fieldParagraphs, fieldUtterances, fieldTopics, fieldSentiment, fieldIntents, fieldDetectEntities, fieldDetectLanguage, fieldSummarize, fieldRedact, fieldKeyterm} {
		if _, exists := params.Extra[field]; exists {
			return nil, fmt.Errorf("deepgram: extension %q extra field %q has a dedicated request field", TranscriptionRequestExtensionKey, field)
		}
	}
	params.Model = options.Model
	params.Language = options.Language
	if params.Summarize == summaryVersion1 {
		return nil, errors.New("deepgram: summarize=v1 is deprecated; use true or v2")
	}
	return &params, nil
}

// newTranscriptionResponse projects the first alternative of the first
// channel; its confidence, words and the utterances become output metadata.
func newTranscriptionResponse(apiResp *listenResponse, model string) (*transcription.Response, error) {
	if len(apiResp.Results.Channels) == 0 || len(apiResp.Results.Channels[0].Alternatives) == 0 {
		return nil, fmt.Errorf("%w: deepgram: response has no transcript alternatives", transcription.ErrInvalidResponse)
	}
	alt := apiResp.Results.Channels[0].Alternatives[0]
	if alt.Transcript == nil {
		return nil, fmt.Errorf("%w: deepgram: transcription requires transcript", transcription.ErrInvalidResponse)
	}
	var outputMetadata metadata.Map
	if alt.Confidence != nil {
		if err := outputMetadata.Set(metadataConfidence, *alt.Confidence); err != nil {
			return nil, err
		}
	}
	if alt.Words != nil {
		if err := outputMetadata.Set(metadataWords, alt.Words); err != nil {
			return nil, err
		}
	}
	if len(apiResp.Results.Utterances) > 0 {
		if err := outputMetadata.Set(metadataUtterances, apiResp.Results.Utterances); err != nil {
			return nil, err
		}
	}
	output, err := transcription.NewOutput(*alt.Transcript, outputMetadata)
	if err != nil {
		return nil, err
	}
	meta := &transcription.ResponseMetadata{Model: model}
	if err := meta.Extra.Set(metadataRequestID, apiResp.Metadata.RequestID); err != nil {
		return nil, err
	}
	if apiResp.Metadata.Duration != nil {
		if err := meta.Extra.Set(metadataDurationSeconds, *apiResp.Metadata.Duration); err != nil {
			return nil, err
		}
	}
	if apiResp.Metadata.Channels != nil {
		if err := meta.Extra.Set(metadataChannels, *apiResp.Metadata.Channels); err != nil {
			return nil, err
		}
	}
	if err := meta.Extra.Set(TranscriptionResponseExtensionKey, apiResp.Raw); err != nil {
		return nil, err
	}
	return transcription.NewResponse(output, meta)
}
