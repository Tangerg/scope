package openai

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"sync"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/speech"
)

type SpeechModelConfig struct {
	Provider         string
	APIKey           string
	DefaultOptions   speech.Options
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes speech.AudioByteLimit
}

func (s SpeechModelConfig) Validate() error {
	if err := validateBinding(s.Provider, s.APIKey, s.BaseURL); err != nil {
		return err
	}
	if s.DefaultOptions.Model == "" {
		return errors.New("openai: DefaultOptions.Model is required")
	}
	if err := s.DefaultOptions.Validate(); err != nil {
		return err
	}
	if err := s.MaxResponseBytes.Validate(); err != nil {
		return fmt.Errorf("openai: MaxResponseBytes: %w", err)
	}
	return nil
}

var _ speech.Model = (*SpeechModel)(nil)
var _ speech.Streamer = (*SpeechModel)(nil)

type SpeechModel struct {
	api              *api
	provider         string
	defaultOptions   speech.Options
	maxResponseBytes int64
}

func NewSpeechModel(ctx context.Context, config SpeechModelConfig) (*SpeechModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	api, err := newAPI(ctx, apiConfig{
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}

	return &SpeechModel{
		api:              api,
		provider:         config.Provider,
		defaultOptions:   config.DefaultOptions.Clone(),
		maxResponseBytes: config.MaxResponseBytes.Bytes(),
	}, nil
}

// Speech request fields Core owns.
const (
	speechFieldModel          = "model"
	speechFieldInput          = "input"
	speechFieldVoice          = "voice"
	speechFieldSpeed          = "speed"
	speechFieldResponseFormat = "response_format"
	speechFieldStreamFormat   = "stream_format"
)

func (s *SpeechModel) buildAPITTSRequest(req *speech.Request) (*openai.AudioSpeechNewParams, error) {
	effectiveOptions, err := s.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	fields, err := decodeRequestFields(effectiveOptions.Extensions, s.provider+SpeechRequestExtensionSuffix, speechFieldModel, speechFieldInput, speechFieldVoice, speechFieldSpeed, speechFieldResponseFormat, speechFieldStreamFormat)
	if err != nil {
		return nil, err
	}
	params := &openai.AudioSpeechNewParams{}
	params.SetExtraFields(fields)

	params.Model = effectiveOptions.Model
	params.Input = req.Text
	if effectiveOptions.Voice != "" {
		params.Voice = openai.AudioSpeechNewParamsVoiceUnion{OfString: param.NewOpt(effectiveOptions.Voice)}
	}
	// OpenAI documents the range as 0.25 to 4.0 and validates the parameter
	// itself, so the value goes as given: a local bound here would only
	// duplicate a check the API already reports on. That is the opposite of
	// the sibling adapters whose providers clamp silently, where refusing
	// locally is the only way the mismatch surfaces.
	if effectiveOptions.Speed != 0 {
		params.Speed = openai.Float(effectiveOptions.Speed)
	}
	if effectiveOptions.OutputFormat != "" {
		params.ResponseFormat = openai.AudioSpeechNewParamsResponseFormat(effectiveOptions.OutputFormat)
	}
	params.StreamFormat = openai.AudioSpeechNewParamsStreamFormatAudio

	return params, nil
}

func (s *SpeechModel) buildTTSResponse(mimeType string, data []byte) (*speech.Response, error) {
	output, err := speech.NewOutput(mimeType, data, nil)
	if err != nil {
		return nil, err
	}
	return speech.NewResponse(output, &speech.ResponseMetadata{})
}

func (s *SpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
	return speech.Collect(s.Stream(ctx, req), s.maxResponseBytes)
}

func (s *SpeechModel) Stream(ctx context.Context, req *speech.Request) iter.Seq2[*speech.Response, error] {
	return func(yield func(*speech.Response, error) bool) {
		if err := req.Validate(); err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
			return
		}
		apiReq, err := s.buildAPITTSRequest(req)
		if err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
			return
		}

		apiResp, err := s.api.speech(ctx, apiReq)
		if err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err})
			return
		}
		closeBody := sync.OnceValue(apiResp.Body.Close)
		defer func() { _ = closeBody() }()
		mimeType := apiResp.Header.Get("Content-Type")

		received := false
		for chunk, err := range speech.ReadChunks(apiResp.Body) {
			if err != nil {
				yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: errors.Join(err, closeBody())})
				return
			}

			resp, err := s.buildTTSResponse(mimeType, chunk)
			if err != nil {
				yield(nil, err)
				return
			}

			received = true
			if !yield(resp, nil) {
				return
			}
		}
		if err := errors.Join(ctx.Err(), closeBody()); err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err})
			return
		}
		if !received {
			yield(nil, fmt.Errorf("openai: %w: speech stream returned no audio", speech.ErrInvalidResponse))
		}
	}
}
