package hume

import (
	"context"
	"errors"
	"net/http"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/speech"
)

const (
	metadataAudioFormat      = "hume/audio_format"
	metadataChunkIndex       = "hume/chunk_index"
	metadataDurationSeconds  = "hume/duration_seconds"
	metadataEncodingFormat   = "hume/encoding_format"
	metadataGenerationID     = "hume/generation_id"
	metadataIsLastChunk      = "hume/is_last_chunk"
	metadataRequestID        = "hume/request_id"
	metadataResponse         = "hume/response"
	metadataSampleRate       = "hume/sample_rate"
	metadataSnippetID        = "hume/snippet_id"
	metadataStreamAudioEvent = "hume/stream_audio_event"
	metadataText             = "hume/text"
	metadataTranscribedText  = "hume/transcribed_text"
	metadataUtteranceIndex   = "hume/utterance_index"
)

// SpeechModelConfig binds provider access and defaults shared by every speech call.
type SpeechModelConfig struct {
	APIKey         string
	DefaultOptions speech.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (s SpeechModelConfig) Validate() error {
	if s.APIKey == "" {
		return errors.New("hume: APIKey is required")
	}
	if s.DefaultOptions.Model == "" {
		return errors.New("hume: DefaultOptions.Model is required")
	}
	if err := s.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ speech.Model = (*SpeechModel)(nil)

// SpeechModel wraps Hume's Octave TTS (/v0/tts). Hume's headline
// feature is emotion-aware synthesis driven by per-utterance
// "description" cues — those live on the extension-threaded provider request.
//
// [speech.Options].Voice maps onto a voice ID; voice.provider defaults to
// HUME_AI and may be set through SpeechRequestExtensionKey.
// [speech.Options].Model selects the official Octave version ("1" or "2").
type SpeechModel struct{ binding *speechBinding }

func NewSpeechModel(ctx context.Context, config SpeechModelConfig) (*SpeechModel, error) {
	binding, err := newSpeechBinding(ctx, config)
	if err != nil {
		return nil, err
	}
	return &SpeechModel{binding: binding}, nil
}

func (s *SpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	body, err := s.binding.buildAPIRequest(req)
	if err != nil {
		return nil, err
	}
	if body.InstantMode != nil {
		return nil, errors.New("hume: speech: instant_mode is only supported by streaming endpoints")
	}
	apiResp, err := s.binding.api.tts(ctx, body)
	if err != nil {
		return nil, err
	}
	return s.buildResponse(apiResp, body.Version)
}

func (s *SpeechModel) buildResponse(apiResp *ttsResponse, model string) (*speech.Response, error) {
	audio, err := apiResp.decodeAudio()
	if err != nil {
		return nil, err
	}
	var outputMetadata metadata.Map
	if len(apiResp.Generations) > 0 {
		g := apiResp.Generations[0]
		if setErr := outputMetadata.Set(metadataEncodingFormat, g.Encoding.Format); setErr != nil {
			return nil, setErr
		}
		if setErr := outputMetadata.Set(metadataSampleRate, g.Encoding.SampleRate); setErr != nil {
			return nil, setErr
		}
		if setErr := outputMetadata.Set(metadataDurationSeconds, g.Duration); setErr != nil {
			return nil, setErr
		}
		if setErr := outputMetadata.Set(metadataGenerationID, g.ID); setErr != nil {
			return nil, setErr
		}
	}
	output, err := speech.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}
	meta := &speech.ResponseMetadata{Model: model}
	if apiResp.RequestID != "" {
		if err := meta.Extra.Set(metadataRequestID, apiResp.RequestID); err != nil {
			return nil, err
		}
	}
	if err := meta.Extra.Set(metadataResponse, apiResp); err != nil {
		return nil, err
	}
	return speech.NewResponse(output, meta)
}
