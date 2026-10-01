package deepgram

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
)

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultMaxResponseBytes   = int64(32 * 1024 * 1024)
	maximumErrorResponseBytes = int64(64 * 1024)
)

// SpeechModelConfig binds provider access and defaults shared by every speech call.
type SpeechModelConfig struct {
	APIKey           string
	DefaultOptions   speech.Options
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (s SpeechModelConfig) Validate() error {
	if s.APIKey == "" {
		return errors.New("deepgram: APIKey is required")
	}
	if s.DefaultOptions.Model == "" {
		return errors.New("deepgram: DefaultOptions.Model is required")
	}
	if err := s.DefaultOptions.Validate(); err != nil {
		return err
	}
	if s.MaxResponseBytes < 0 {
		return errors.New("deepgram: MaxResponseBytes must not be negative")
	}
	return nil
}

var _ speech.Model = (*SpeechModel)(nil)
var _ speech.Streamer = (*SpeechModel)(nil)

// SpeechModel wraps Deepgram's /v1/speak endpoint. Supported models
// include the Aura family ("aura-asteria-en", "aura-luna-en", ...) and
// Aura-2 ("aura-2-thalia-en"); Deepgram uses model+voice fused as one
// id, so [speech.Options].Voice is unused and [speech.Options].Model carries
// the full picker.
type SpeechModel struct {
	api            *api
	defaultOptions speech.Options
}

// NewSpeechModel rejects an invalid provider binding before the first speech call.
func NewSpeechModel(_ context.Context, config SpeechModelConfig) (*SpeechModel, error) {
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

	return &SpeechModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (s *SpeechModel) buildAPIRequest(req *speech.Request) (string, *speakParams, error) {
	effectiveOptions, err := s.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, err
	}
	if effectiveOptions.Voice != "" {
		return "", nil, errors.New("deepgram: speech: unsupported option: voice")
	}

	nativeFields, _, decodeErr := effectiveOptions.Extensions.Decode[map[string]any](SpeechRequestExtensionKey)
	if decodeErr != nil {
		return "", nil, decodeErr
	}
	for _, field := range []string{"model", "encoding", "container", "speed", "text"} {
		if _, exists := nativeFields[field]; exists {
			return "", nil, fmt.Errorf("deepgram: extension %q field %q is owned by Core", SpeechRequestExtensionKey, field)
		}
	}

	paramsValue, _, err := effectiveOptions.Extensions.Decode[speakParams](SpeechRequestExtensionKey)

	params := &paramsValue
	if err != nil {
		return "", nil, err
	}
	for _, field := range []string{"model", "encoding", "container", "sample_rate", "bit_rate", "speed", "text"} {
		if _, exists := params.Extra[field]; exists {
			return "", nil, fmt.Errorf("deepgram: extension %q extra field %q has a dedicated request field", SpeechRequestExtensionKey, field)
		}
	}
	params.Model = effectiveOptions.Model
	if effectiveOptions.OutputFormat != "" {
		switch effectiveOptions.OutputFormat {
		case "wav":
			params.Encoding = "linear16"
			params.Container = "wav"
		case "mp3", "flac", "aac", "opus", "mulaw", "alaw", "linear16":
			params.Encoding = effectiveOptions.OutputFormat
		default:
			return "", nil, fmt.Errorf("deepgram: speech: unsupported output format %q", effectiveOptions.OutputFormat)
		}
	}
	// Deepgram documents the speed range as 0.7 to 1.5 for the Aura-2 voices
	// this endpoint serves, and narrows the recommendation to 0.9 to 1.5 for
	// Spanish. The wider documented bound is the one enforced; a per-language
	// recommendation is guidance for the caller, not a limit of the API.
	if effectiveOptions.Speed != 0 {
		if effectiveOptions.Speed < 0.7 || effectiveOptions.Speed > 1.5 {
			return "", nil, errors.New("deepgram: speech: speed must be between 0.7 and 1.5")
		}
		params.Speed = effectiveOptions.Speed
	}

	return req.Text, params, nil
}

func (s *SpeechModel) buildResponse(audio []byte, hdr http.Header) (*speech.Response, error) {
	if len(audio) == 0 {
		return nil, errors.New("deepgram: speech response contained no audio")
	}
	var outputMetadata metadata.Map
	if ct := hdr.Get("Content-Type"); ct != "" {
		if err := outputMetadata.Set("deepgram/mime_type", ct); err != nil {
			return nil, err
		}
	}

	output, err := speech.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}
	meta := &speech.ResponseMetadata{Model: hdr.Get("dg-model-name")}
	if requestID := hdr.Get("dg-request-id"); requestID != "" {
		if err := meta.Extra.Set("deepgram/request_id", requestID); err != nil {
			return nil, err
		}
	}
	if err := meta.Extra.Set(SpeechResponseExtensionKey, map[string]string{
		"content_type": hdr.Get("Content-Type"),
		"model_name":   hdr.Get("dg-model-name"),
		"request_id":   hdr.Get("dg-request-id"),
	}); err != nil {
		return nil, err
	}
	return speech.NewResponse(output, meta)
}

func (s *SpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
	var audio []byte
	var response *speech.Response
	for chunk, err := range s.Stream(ctx, req) {
		if err != nil {
			return nil, err
		}
		if int64(len(chunk.Output.Audio)) > s.api.maxResponseBytes-int64(len(audio)) {
			return nil, fmt.Errorf("deepgram: speech response exceeds %d-byte limit", s.api.maxResponseBytes)
		}
		audio = append(audio, chunk.Output.Audio...)
		response = chunk
	}
	if response == nil {
		return nil, fmt.Errorf("deepgram: %w: speech stream returned no audio", speech.ErrInvalidResponse)
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
		text, params, err := s.buildAPIRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		body, hdr, err := s.api.speakStream(ctx, text, params)
		if err != nil {
			yield(nil, err)
			return
		}
		defer body.Close()

		for chunk, err := range readAudioChunks(body) {
			if err != nil {
				yield(nil, err)
				return
			}
			out, err := s.buildResponse(chunk, hdr)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(out, nil) {
				return
			}
		}
	}
}
