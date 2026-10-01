package openai

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/Tangerg/scope/core/speech"
)

// DefaultMaxResponseBytes bounds how much synthesized audio is read into
// memory. A provider that returns an unexpectedly large asset would otherwise
// be able to exhaust the process before the response is ever validated.
const DefaultMaxResponseBytes = int64(32 * 1024 * 1024)

// SpeechModelConfig binds provider access and defaults shared by every speech call.
type SpeechModelConfig struct {
	Provider         string
	APIKey           string
	DefaultOptions   speech.Options
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (s SpeechModelConfig) Validate() error {
	if err := validateProvider(s.Provider); err != nil {
		return fmt.Errorf("openai: Provider: %w", err)
	}
	if err := (apiConfig{APIKey: s.APIKey, BaseURL: s.BaseURL}).validate(); err != nil {
		return err
	}
	if s.DefaultOptions.Model == "" {
		return errors.New("openai: DefaultOptions.Model is required")
	}
	if err := s.DefaultOptions.Validate(); err != nil {
		return err
	}
	if s.MaxResponseBytes < 0 {
		return errors.New("openai: MaxResponseBytes must not be negative")
	}
	return nil
}

var _ speech.Model = (*SpeechModel)(nil)
var _ speech.Streamer = (*SpeechModel)(nil)

// SpeechModel implements the OpenAI-compatible speech protocol.
type SpeechModel struct {
	api              *api
	provider         string
	defaultOptions   speech.Options
	maxResponseBytes int64
}

// NewSpeechModel rejects an invalid provider binding before the first speech call.
func NewSpeechModel(_ context.Context, config SpeechModelConfig) (*SpeechModel, error) {
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

	return &SpeechModel{
		api:              api,
		provider:         config.Provider,
		defaultOptions:   config.DefaultOptions.Clone(),
		maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes),
	}, nil
}

func (s *SpeechModel) buildAPITTSRequest(req *speech.Request) (*openai.AudioSpeechNewParams, error) {
	effectiveOptions, err := s.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	fields, err := decodeRequestFields(effectiveOptions.Extensions, protocolModalityRequestExtensionKey(s.provider, "speech"), "model", "input", "voice", "speed", "response_format", "stream_format")
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

func (s *SpeechModel) buildTTSResponse(data []byte) (*speech.Response, error) {
	output, err := speech.NewOutput(data, nil)
	if err != nil {
		return nil, err
	}
	return speech.NewResponse(output, &speech.ResponseMetadata{})
}

func (s *SpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
	var audio []byte
	var response *speech.Response
	for chunk, err := range s.Stream(ctx, req) {
		if err != nil {
			return nil, err
		}
		if int64(len(chunk.Output.Audio)) > s.maxResponseBytes-int64(len(audio)) {
			return nil, fmt.Errorf("openai: speech response exceeds %d-byte limit", s.maxResponseBytes)
		}
		audio = append(audio, chunk.Output.Audio...)
		response = chunk
	}
	if response == nil {
		return nil, fmt.Errorf("openai: %w: speech stream returned no audio", speech.ErrInvalidResponse)
	}
	response.Output.Audio = audio
	return response, nil
}

func (s *SpeechModel) Stream(ctx context.Context, req *speech.Request) iter.Seq2[*speech.Response, error] {
	return func(yield func(*speech.Response, error) bool) {
		if err := req.Validate(); err != nil {
			yield(nil, err)
			return
		}
		apiReq, err := s.buildAPITTSRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}

		apiResp, err := s.api.speech(ctx, apiReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer apiResp.Body.Close()

		for chunk, err := range readAudioChunks(apiResp.Body) {
			if err != nil {
				yield(nil, err)
				return
			}

			resp, err := s.buildTTSResponse(chunk)
			if err != nil {
				yield(nil, err)
				return
			}

			if !yield(resp, nil) {
				return
			}
		}
	}
}

func readAudioChunks(reader io.Reader) iter.Seq2[[]byte, error] {
	const chunkSize = 16 * 1024
	return func(yield func([]byte, error) bool) {
		for {
			buffer := make([]byte, chunkSize)
			read, err := reader.Read(buffer)
			// Reader may return both bytes and an error; deliver the bytes first.
			if read > 0 && !yield(buffer[:read], nil) {
				return
			}
			if err != nil {
				if err != io.EOF {
					yield(nil, err)
				}
				return
			}
		}
	}
}
