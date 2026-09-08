package deepgram

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/Tangerg/scope/core/metadata"
	tts "github.com/Tangerg/scope/core/speech"
)

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultMaxResponseBytes   = int64(32 * 1024 * 1024)
	maximumErrorResponseBytes = int64(64 * 1024)
)

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	APIKey           string
	DefaultOptions   tts.Options
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (a AudioTTSModelConfig) Validate() error {
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

var _ tts.Model = (*AudioTTSModel)(nil)
var _ tts.Streamer = (*AudioTTSModel)(nil)

// AudioTTSModel wraps Deepgram's /v1/speak endpoint. Supported models
// include the Aura family ("aura-asteria-en", "aura-luna-en", ...) and
// Aura-2 ("aura-2-thalia-en"); Deepgram uses model+voice fused as one
// id, so [tts.Options].Voice is unused and [tts.Options].Model carries
// the full picker.
type AudioTTSModel struct {
	api            *api
	defaultOptions tts.Options
}

// NewAudioTTSModel rejects an invalid provider binding before the first speech call.
func NewAudioTTSModel(config AudioTTSModelConfig) (*AudioTTSModel, error) {
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

	return &AudioTTSModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (a *AudioTTSModel) buildAPIRequest(req *tts.Request) (string, *speakParams, error) {
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return "", nil, err
	}
	if effectiveOptions.Voice != "" {
		return "", nil, errors.New("deepgram: speech: unsupported option: voice")
	}

	paramsValue, _, err := effectiveOptions.Extensions.Decode[speakParams](SpeechRequestExtensionKey)

	params := &paramsValue
	if err != nil {
		return "", nil, err
	}
	if params.Model == "" {
		params.Model = effectiveOptions.Model
	}
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

func (a *AudioTTSModel) buildResponse(audio []byte, hdr http.Header) (*tts.Response, error) {
	if len(audio) == 0 {
		return nil, errors.New("deepgram: speech response contained no audio")
	}
	var outputMetadata metadata.Map
	if ct := hdr.Get("Content-Type"); ct != "" {
		if err := outputMetadata.Set("deepgram/mime_type", ct); err != nil {
			return nil, err
		}
	}

	output, err := tts.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}
	meta := &tts.ResponseMetadata{Model: hdr.Get("dg-model-name")}
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
	return tts.NewResponse(output, meta)
}

func (a *AudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	text, params, err := a.buildAPIRequest(req)
	if err != nil {
		return nil, err
	}

	audio, hdr, err := a.api.speak(ctx, text, params)
	if err != nil {
		return nil, err
	}

	return a.buildResponse(audio, hdr)
}

func (a *AudioTTSModel) Stream(ctx context.Context, req *tts.Request) iter.Seq2[*tts.Response, error] {
	return func(yield func(*tts.Response, error) bool) {
		if err := req.Validate(); err != nil {
			yield(nil, err)
			return
		}
		text, params, err := a.buildAPIRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		body, hdr, err := a.api.speakStream(ctx, text, params)
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
			out, err := a.buildResponse(chunk, hdr)
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
