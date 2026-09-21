package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"google.golang.org/genai"

	tts "github.com/Tangerg/scope/core/speech"
)

var _ tts.Streamer = (*StreamingAudioTTSModel)(nil)
var _ tts.Model = (*StreamingAudioTTSModel)(nil)

// StreamingAudioTTSModel exposes the native incremental audio protocol.
type StreamingAudioTTSModel struct{ binding *speechBinding }

func NewStreamingAudioTTSModel(ctx context.Context, config AudioTTSModelConfig) (*StreamingAudioTTSModel, error) {
	if config.DefaultOptions.Model != ModelGemini31FlashTTSPreview {
		return nil, fmt.Errorf("google: streaming speech requires model %q", ModelGemini31FlashTTSPreview)
	}
	binding, err := newSpeechBinding(ctx, config)
	if err != nil {
		return nil, err
	}
	return &StreamingAudioTTSModel{binding: binding}, nil
}

// Call consumes the same stream to completion. Failed or interrupted synthesis
// never returns partial audio as a complete response.
func (s *StreamingAudioTTSModel) Call(ctx context.Context, req *tts.Request) (*tts.Response, error) {
	var audio []byte
	var response *tts.Response
	for chunk, err := range s.Stream(ctx, req) {
		if err != nil {
			return nil, err
		}
		audio = append(audio, chunk.Output.Audio...)
		response = chunk
	}
	if response == nil {
		return nil, fmt.Errorf("google: %w: speech stream returned no audio", tts.ErrInvalidResponse)
	}
	response.Output.Audio = audio
	return response, nil
}

func (s *StreamingAudioTTSModel) Stream(ctx context.Context, req *tts.Request) iter.Seq2[*tts.Response, error] {
	return func(yield func(*tts.Response, error) bool) {
		if err := req.Validate(); err != nil {
			yield(nil, err)
			return
		}
		modelName, contents, config, err := s.binding.buildAPITTSRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		if modelName != ModelGemini31FlashTTSPreview {
			yield(nil, fmt.Errorf("google: speech: model %q does not support streaming; use %q", modelName, ModelGemini31FlashTTSPreview))
			return
		}

		var pending *tts.Response
		finished := false
		var servedModel string
		for chunk, err := range s.binding.api.chatCompletionStream(ctx, modelName, contents, config) {
			if err != nil {
				yield(nil, err)
				return
			}

			if chunk == nil {
				yield(nil, fmt.Errorf("google: %w: nil speech chunk", tts.ErrInvalidResponse))
				return
			}
			if chunk.ModelVersion != "" {
				servedModel = chunk.ModelVersion
			}
			resp, err := s.binding.buildTTSResponse(chunk)
			if err != nil {
				if !errors.Is(err, errNoAudio) {
					yield(nil, err)
					return
				}
			} else {
				if finished {
					yield(nil, fmt.Errorf("google: %w: audio after speech completion", tts.ErrInvalidResponse))
					return
				}
				if pending != nil && !yield(pending, nil) {
					return
				}
				pending = resp
			}
			if len(chunk.Candidates) > 0 && chunk.Candidates[0].FinishReason != "" {
				if chunk.Candidates[0].FinishReason != genai.FinishReasonStop {
					yield(nil, fmt.Errorf("google: %w: speech ended with %s", tts.ErrInvalidResponse, chunk.Candidates[0].FinishReason))
					return
				}
				finished = true
			}
			// Retaining the final audio chunk lets terminal metadata travel with
			// valid audio without introducing empty Core speech responses.
			if pending != nil {
				pending.Metadata.Model = servedModel
				if err := pending.Metadata.Extra.Set(protocolKey(s.binding.provider, "speech_response"), chunk); err != nil {
					yield(nil, err)
					return
				}
			}
		}
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		if !finished {
			yield(nil, fmt.Errorf("google: speech stream: %w", io.ErrUnexpectedEOF))
			return
		}
		if pending == nil {
			yield(nil, fmt.Errorf("google: %w: speech stream returned no audio", tts.ErrInvalidResponse))
			return
		}
		yield(pending, nil)
	}
}
