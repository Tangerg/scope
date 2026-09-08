package elevenlabs

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/Tangerg/scope/core/metadata"
	tts "github.com/Tangerg/scope/core/speech"
)

const (
	characterCostHeader   = "character-cost"
	contentTypeHeader     = "Content-Type"
	requestIDHeader       = "request-id"
	traceIDHeader         = "x-trace-id"
	metadataCharacterCost = "elevenlabs/character_cost"
	metadataMIMEType      = "elevenlabs/mime_type"
	metadataRequestID     = "elevenlabs/request_id"
	metadataTraceID       = "elevenlabs/trace_id"
)

type speechResponseMetadata struct {
	ContentType   string `json:"content_type"`
	CharacterCost string `json:"character_cost"`
	RequestID     string `json:"request_id"`
	TraceID       string `json:"trace_id"`
}

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	APIKey         string
	DefaultOptions tts.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (a AudioTTSModelConfig) Validate() error {
	if a.APIKey == "" {
		return errors.New("elevenlabs: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("elevenlabs: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ tts.Model = (*AudioTTSModel)(nil)
var _ tts.Streamer = (*AudioTTSModel)(nil)

// AudioTTSModel wraps ElevenLabs' /text-to-speech endpoint.
//
// ElevenLabs is voice-first: every call needs a voice id (the cloned /
// professional voice that says the text), so [tts.Options].Voice is
// required. [tts.Options].Model maps to ElevenLabs' model_id (e.g.
// "eleven_v3", "eleven_multilingual_v2") which selects the synthesis
// engine.
type AudioTTSModel struct {
	api            *api
	defaultOptions tts.Options
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
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (a *AudioTTSModel) buildAPIRequest(req *tts.Request) (voiceID, outputFormat string, body *ttsRequest, err error) {
	effectiveOptions, resolveErr := a.defaultOptions.Resolve(req.Options)
	if resolveErr != nil {
		return "", "", nil, resolveErr
	}

	if effectiveOptions.Voice == "" {
		return "", "", nil, errors.New("elevenlabs: Voice (voice id) is required - set Options.Voice")
	}

	bodyValue, _, err := effectiveOptions.Extensions.Decode[ttsRequest](SpeechRequestExtensionKey)
	if err != nil {
		return "", "", nil, err
	}
	body = &bodyValue
	body.Text = req.Text
	body.ModelID = effectiveOptions.Model

	// ElevenLabs documents the speed range as 0.7 to 1.2 and clamps a value
	// outside it to the nearest limit rather than reporting one, so refusing
	// here is the only way a caller who asked for 2.0 learns they got 1.2.
	if effectiveOptions.Speed != 0 {
		if effectiveOptions.Speed < 0.7 || effectiveOptions.Speed > 1.2 {
			return "", "", nil, fmt.Errorf("elevenlabs: speech speed must be between 0.7 and 1.2, got %g", effectiveOptions.Speed)
		}
		if body.VoiceSettings == nil {
			body.VoiceSettings = &voiceSettings{}
		}
		v := effectiveOptions.Speed
		body.VoiceSettings.Speed = &v
	}
	if body.OptimizeStreamingLatency != nil && (*body.OptimizeStreamingLatency < 0 || *body.OptimizeStreamingLatency > 4) {
		return "", "", nil, fmt.Errorf("elevenlabs: optimize_streaming_latency must be between 0 and 4, got %d", *body.OptimizeStreamingLatency)
	}
	if effectiveOptions.OutputFormat != "" && !isSupportedOutputFormat(effectiveOptions.OutputFormat) {
		return "", "", nil, fmt.Errorf("elevenlabs: unsupported output format %q", effectiveOptions.OutputFormat)
	}

	return effectiveOptions.Voice, effectiveOptions.OutputFormat, body, nil
}

func (a *AudioTTSModel) buildResponse(audio []byte, hdr http.Header) (*tts.Response, error) {
	if len(audio) == 0 {
		return nil, errors.New("elevenlabs: speech response contained no audio")
	}
	var outputMetadata metadata.Map
	details := speechResponseMetadata{
		ContentType:   hdr.Get(contentTypeHeader),
		CharacterCost: hdr.Get(characterCostHeader),
		RequestID:     hdr.Get(requestIDHeader),
		TraceID:       hdr.Get(traceIDHeader),
	}
	if details.ContentType != "" {
		if err := outputMetadata.Set(metadataMIMEType, details.ContentType); err != nil {
			return nil, err
		}
	}

	output, err := tts.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}

	responseMetadata := &tts.ResponseMetadata{}
	if details.CharacterCost != "" {
		if err := responseMetadata.Extra.Set(metadataCharacterCost, details.CharacterCost); err != nil {
			return nil, err
		}
	}
	if details.RequestID != "" {
		if err := responseMetadata.Extra.Set(metadataRequestID, details.RequestID); err != nil {
			return nil, err
		}
	}
	if details.TraceID != "" {
		if err := responseMetadata.Extra.Set(metadataTraceID, details.TraceID); err != nil {
			return nil, err
		}
	}
	if err := responseMetadata.Extra.Set(SpeechResponseExtensionKey, details); err != nil {
		return nil, err
	}
	return tts.NewResponse(output, responseMetadata)
}

func isSupportedOutputFormat(format string) bool {
	switch format {
	case "alaw_8000",
		"mp3_22050_32", "mp3_24000_48", "mp3_44100_32", "mp3_44100_64", "mp3_44100_96", "mp3_44100_128", "mp3_44100_192",
		"opus_48000_32", "opus_48000_64", "opus_48000_96", "opus_48000_128", "opus_48000_192",
		"pcm_8000", "pcm_16000", "pcm_22050", "pcm_24000", "pcm_32000", "pcm_44100", "pcm_48000",
		"ulaw_8000",
		"wav_8000", "wav_16000", "wav_22050", "wav_24000", "wav_32000", "wav_44100", "wav_48000":
		return true
	default:
		return false
	}
}

func (a *AudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	voiceID, outputFormat, body, err := a.buildAPIRequest(req)
	if err != nil {
		return nil, err
	}

	audio, hdr, err := a.api.textToSpeech(ctx, voiceID, outputFormat, body)
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
		voiceID, outputFormat, body, err := a.buildAPIRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}

		body_, hdr, err := a.api.textToSpeechStream(ctx, voiceID, outputFormat, body)
		if err != nil {
			yield(nil, err)
			return
		}
		defer body_.Close()

		for chunk, err := range readAudioChunks(body_) {
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
