package hume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"

	"github.com/Tangerg/scope/core/metadata"
	tts "github.com/Tangerg/scope/core/speech"
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

// AudioTTSModelConfig binds provider access and defaults shared by every speech call.
type AudioTTSModelConfig struct {
	APIKey         string
	DefaultOptions tts.Options
	BaseURL        string
	HTTPClient     *http.Client
}

func (a AudioTTSModelConfig) Validate() error {
	if a.APIKey == "" {
		return errors.New("hume: APIKey is required")
	}
	if a.DefaultOptions.Model == "" {
		return errors.New("hume: DefaultOptions.Model is required")
	}
	if err := a.DefaultOptions.Validate(); err != nil {
		return err
	}
	return nil
}

var _ tts.Model = (*AudioTTSModel)(nil)
var _ tts.Streamer = (*AudioTTSModel)(nil)

// AudioTTSModel wraps Hume's Octave TTS (/v0/tts). Hume's headline
// feature is emotion-aware synthesis driven by per-utterance
// "description" cues — those live on the extension-threaded provider request.
//
// [tts.Options].Voice maps onto a HUME_AI voice id and
// [tts.Options].Model selects the official Octave version ("1" or "2").
type AudioTTSModel struct {
	api            *api
	defaultOptions tts.Options
}

// NewAudioTTSModel rejects an invalid provider binding before the first speech call.
func NewAudioTTSModel(_ context.Context, config AudioTTSModelConfig) (*AudioTTSModel, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	api, err := newAPI(apiConfig{APIKey: config.APIKey, BaseURL: config.BaseURL, HTTPClient: config.HTTPClient})
	if err != nil {
		return nil, err
	}
	return &AudioTTSModel{api: api, defaultOptions: config.DefaultOptions.Clone()}, nil
}

func (a *AudioTTSModel) buildAPIRequest(req *tts.Request, streaming bool) (*ttsRequest, error) {
	effectiveOptions, err := a.defaultOptions.Resolve(req.Options)
	if err != nil {
		return nil, err
	}

	bodyValue, _, err := effectiveOptions.Extensions.Decode[ttsRequest](SpeechRequestExtensionKey)

	body := &bodyValue
	if err != nil {
		return nil, err
	}
	if effectiveOptions.Model != ModelOctave1 && effectiveOptions.Model != ModelOctave2 {
		return nil, fmt.Errorf("hume: speech: model must be %q or %q", ModelOctave1, ModelOctave2)
	}
	if body.NumGenerations > 1 {
		return nil, errors.New("hume: speech: num_generations greater than 1 cannot be represented by Core's single-output response")
	}
	if len(body.Utterances) == 0 {
		body.Utterances = []utterance{{}}
	}
	body.Utterances[0].Text = req.Text
	if effectiveOptions.Voice != "" {
		body.Utterances[0].Voice = &voice{ID: effectiveOptions.Voice, Provider: "HUME_AI"}
	}
	if effectiveOptions.Speed != 0 {
		v := effectiveOptions.Speed
		body.Utterances[0].Speed = &v
	}
	body.Version = effectiveOptions.Model
	if effectiveOptions.OutputFormat != "" {
		switch effectiveOptions.OutputFormat {
		case "mp3", "wav", "pcm":
		default:
			return nil, errors.New("hume: speech: output_format must be mp3, wav, or pcm")
		}
		body.Format = map[string]any{"type": effectiveOptions.OutputFormat}
	}
	if body.Version == ModelOctave2 && body.Utterances[0].Voice == nil {
		return nil, errors.New("hume: speech: Octave 2 requires Options.Voice or a voice on the first utterance")
	}
	if !streaming && body.InstantMode != nil {
		return nil, errors.New("hume: speech: instant_mode is only supported by streaming endpoints")
	}
	if streaming && body.Utterances[0].Voice == nil && body.InstantMode == nil {
		instantMode := false
		body.InstantMode = &instantMode
	}
	return body, nil
}

func (a *AudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	body, err := a.buildAPIRequest(req, false)
	if err != nil {
		return nil, err
	}
	apiResp, err := a.api.tts(ctx, body)
	if err != nil {
		return nil, err
	}
	return a.buildResponse(apiResp, body.Version)
}

func (a *AudioTTSModel) buildResponse(apiResp *ttsResponse, model string) (*tts.Response, error) {
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
	output, err := tts.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}
	meta := &tts.ResponseMetadata{Model: model}
	if apiResp.RequestID != "" {
		if err := meta.Extra.Set(metadataRequestID, apiResp.RequestID); err != nil {
			return nil, err
		}
	}
	if err := meta.Extra.Set(metadataResponse, apiResp); err != nil {
		return nil, err
	}
	return tts.NewResponse(output, meta)
}

func (a *AudioTTSModel) Stream(ctx context.Context, req *tts.Request) iter.Seq2[*tts.Response, error] {
	return func(yield func(*tts.Response, error) bool) {
		if err := req.Validate(); err != nil {
			yield(nil, err)
			return
		}
		request, err := a.buildAPIRequest(req, true)
		if err != nil {
			yield(nil, err)
			return
		}
		if len(request.IncludeTimestampTypes) != 0 {
			yield(nil, errors.New("hume: speech stream: include_timestamp_types cannot be represented by Core audio-only stream responses"))
			return
		}
		body, err := a.api.ttsStream(ctx, request)
		if err != nil {
			yield(nil, err)
			return
		}
		defer body.Close()

		decoder := json.NewDecoder(body)
		for {
			var event ttsStreamEvent
			if err := decoder.Decode(&event); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				if contextErr := ctx.Err(); contextErr != nil {
					yield(nil, contextErr)
					return
				}
				yield(nil, fmt.Errorf("hume: decode streamed JSON response: %w", err))
				return
			}
			if event.Type != "audio" {
				yield(nil, fmt.Errorf("hume: unexpected streamed event type %q", event.Type))
				return
			}
			response, err := event.response(request.Version)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(response, nil) {
				return
			}
		}
	}
}
