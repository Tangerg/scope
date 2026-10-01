package elevenlabs

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
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

// SpeechModelConfig binds provider access and defaults shared by every speech call.
type SpeechModelConfig struct {
	APIKey         string
	DefaultOptions speech.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (s SpeechModelConfig) Validate() error {
	if s.APIKey == "" {
		return errors.New("elevenlabs: APIKey is required")
	}
	if s.DefaultOptions.Model == "" {
		return errors.New("elevenlabs: DefaultOptions.Model is required")
	}
	if err := s.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ speech.Model = (*SpeechModel)(nil)
var _ speech.Streamer = (*SpeechModel)(nil)

// SpeechModel wraps ElevenLabs' /text-to-speech endpoint.
//
// ElevenLabs is voice-first: every call needs a voice id (the cloned /
// professional voice that says the text), so [speech.Options].Voice is
// required. [speech.Options].Model maps to ElevenLabs' model_id (e.g.
// "eleven_v3", "eleven_multilingual_v2") which selects the synthesis
// engine.
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
		APIKey:     config.APIKey,
		BaseURL:    config.BaseURL,
		HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, err
	}

	return &SpeechModel{
		api:            api,
		defaultOptions: config.DefaultOptions.Clone(),
	}, nil
}

func (s *SpeechModel) buildAPIRequest(req *speech.Request) (voiceID, outputFormat string, body *ttsRequest, err error) {
	effectiveOptions, resolveErr := s.defaultOptions.Resolve(req.Options)
	if resolveErr != nil {
		return "", "", nil, resolveErr
	}

	if effectiveOptions.Voice == "" {
		return "", "", nil, errors.New("elevenlabs: Voice (voice id) is required - set Options.Voice")
	}

	nativeFields, _, err := effectiveOptions.Extensions.Decode[map[string]any](SpeechRequestExtensionKey)
	if err != nil {
		return "", "", nil, err
	}
	for _, field := range []string{"text", "model_id", "voice_id", "output_format"} {
		if _, exists := nativeFields[field]; exists {
			return "", "", nil, fmt.Errorf("elevenlabs: extension %q field %q is owned by Core", SpeechRequestExtensionKey, field)
		}
	}
	if settings, ok := nativeFields["voice_settings"].(map[string]any); ok {
		if _, exists := settings["speed"]; exists {
			return "", "", nil, fmt.Errorf("elevenlabs: extension %q field voice_settings.speed is owned by Core", SpeechRequestExtensionKey)
		}
	}

	bodyValue, _, err := effectiveOptions.Extensions.Decode[ttsRequest](SpeechRequestExtensionKey)
	if err != nil {
		return "", "", nil, err
	}
	body = &bodyValue
	body.Body.Text = req.Text
	body.Body.ModelID = effectiveOptions.Model

	// ElevenLabs documents the speed range as 0.7 to 1.2 and clamps a value
	// outside it to the nearest limit rather than reporting one, so refusing
	// here is the only way a caller who asked for 2.0 learns they got 1.2.
	if effectiveOptions.Speed != 0 {
		if effectiveOptions.Speed < 0.7 || effectiveOptions.Speed > 1.2 {
			return "", "", nil, fmt.Errorf("elevenlabs: speech speed must be between 0.7 and 1.2, got %g", effectiveOptions.Speed)
		}
		if body.Body.VoiceSettings == nil {
			body.Body.VoiceSettings = &voiceSettings{}
		}
		v := effectiveOptions.Speed
		body.Body.VoiceSettings.Speed = &v
	}
	if body.OptimizeStreamingLatency != nil && (*body.OptimizeStreamingLatency < 0 || *body.OptimizeStreamingLatency > 4) {
		return "", "", nil, fmt.Errorf("elevenlabs: optimize_streaming_latency must be between 0 and 4, got %d", *body.OptimizeStreamingLatency)
	}
	if effectiveOptions.OutputFormat != "" && !isSupportedOutputFormat(effectiveOptions.OutputFormat) {
		return "", "", nil, fmt.Errorf("elevenlabs: unsupported output format %q", effectiveOptions.OutputFormat)
	}

	return effectiveOptions.Voice, effectiveOptions.OutputFormat, body, nil
}

func (s *SpeechModel) buildResponse(audio []byte, hdr http.Header) (*speech.Response, error) {
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

	output, err := speech.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}

	responseMetadata := &speech.ResponseMetadata{}
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
	return speech.NewResponse(output, responseMetadata)
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

func (s *SpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
	var audio []byte
	var response *speech.Response
	for chunk, err := range s.Stream(ctx, req) {
		if err != nil {
			return nil, err
		}

		audio = append(audio, chunk.Output.Audio...)
		response = chunk
	}
	if response == nil {
		return nil, fmt.Errorf("elevenlabs: %w: speech stream returned no audio", speech.ErrInvalidResponse)
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
		voiceID, outputFormat, body, err := s.buildAPIRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}

		body_, hdr, err := s.api.textToSpeechStream(ctx, voiceID, outputFormat, body)
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
