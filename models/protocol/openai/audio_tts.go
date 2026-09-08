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

	tts "github.com/Tangerg/scope/core/speech"
)

// DefaultMaxResponseBytes bounds how much synthesized audio is read into
// memory. A provider that returns an unexpectedly large asset would otherwise
// be able to exhaust the process before the response is ever validated.
const DefaultMaxResponseBytes = int64(32 * 1024 * 1024)

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	Provider         string
	APIKey           string
	DefaultOptions   tts.Options
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (a AudioTTSModelConfig) Validate() error {
	if err := validateProvider(a.Provider); err != nil {
		return fmt.Errorf("openai: Provider: %w", err)
	}
	if a.APIKey == "" {
		return errors.New("openai: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("openai: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	if a.MaxResponseBytes < 0 {
		return errors.New("openai: MaxResponseBytes must not be negative")
	}
	return nil
}

var _ tts.Model = (*AudioTTSModel)(nil)
var _ tts.Streamer = (*AudioTTSModel)(nil)

// AudioTTSModel implements the OpenAI-compatible speech protocol.
type AudioTTSModel struct {
	api              *api
	provider         string
	defaultOptions   tts.Options
	maxResponseBytes int64
}

// NewAudioTTSModel rejects an invalid provider binding before the first speech call.
func NewAudioTTSModel(_ context.Context, config AudioTTSModelConfig) (*AudioTTSModel, error) {
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

	return &AudioTTSModel{
		api:              api,
		provider:         config.Provider,
		defaultOptions:   config.DefaultOptions.Clone(),
		maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes),
	}, nil
}

func (a *AudioTTSModel) buildAPITTSRequest(req *tts.Request) (*openai.AudioSpeechNewParams, error) {
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	fields, err := decodeRequestFields(effectiveOptions.Extensions, protocolModalityRequestExtensionKey(a.provider, "speech"), "model", "input", "voice", "speed", "response_format", "stream_format")
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

func (a *AudioTTSModel) buildTTSResponse(data []byte) (*tts.Response, error) {
	output, err := tts.NewOutput(data, nil)
	if err != nil {
		return nil, err
	}
	return tts.NewResponse(output, &tts.ResponseMetadata{})
}

func (a *AudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	apiReq, err := a.buildAPITTSRequest(req)
	if err != nil {
		return nil, err
	}

	apiResp, err := a.api.audioTTS(ctx, apiReq)
	if err != nil {
		return nil, err
	}
	defer apiResp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(apiResp.Body, a.maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > a.maxResponseBytes {
		return nil, fmt.Errorf("openai: speech response exceeds %d-byte limit", a.maxResponseBytes)
	}

	return a.buildTTSResponse(data)
}

func (a *AudioTTSModel) Stream(ctx context.Context, req *tts.Request) iter.Seq2[*tts.Response, error] {
	return func(yield func(*tts.Response, error) bool) {
		if err := req.Validate(); err != nil {
			yield(nil, err)
			return
		}
		apiReq, err := a.buildAPITTSRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}

		apiResp, err := a.api.audioTTS(ctx, apiReq)
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

			resp, err := a.buildTTSResponse(chunk)
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
