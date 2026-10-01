package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/Tangerg/scope/core/speech"
)

var _ speech.Streamer = (*StreamingSpeechModel)(nil)
var _ speech.Model = (*StreamingSpeechModel)(nil)

// StreamingSpeechModel exposes the native incremental audio protocol.
type StreamingSpeechModel struct{ binding *speechBinding }

func NewStreamingSpeechModel(ctx context.Context, config SpeechModelConfig) (*StreamingSpeechModel, error) {
	if config.DefaultOptions.Model != ModelGemini31FlashTTSPreview {
		return nil, fmt.Errorf("google: streaming speech requires model %q", ModelGemini31FlashTTSPreview)
	}
	binding, err := newSpeechBinding(ctx, config)
	if err != nil {
		return nil, err
	}
	return &StreamingSpeechModel{binding: binding}, nil
}

// Call consumes the same stream to completion. Failed or interrupted synthesis
// never returns partial audio as a complete response.
func (s *StreamingSpeechModel) Call(ctx context.Context, req *speech.Request) (*speech.Response, error) {
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
		return nil, fmt.Errorf("google: %w: speech stream returned no audio", speech.ErrInvalidResponse)
	}
	response.Output.Audio = audio
	return response, nil
}

func (s *StreamingSpeechModel) Stream(ctx context.Context, req *speech.Request) iter.Seq2[*speech.Response, error] {
	return func(yield func(*speech.Response, error) bool) {
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

		var pending *speech.Response
		finished := false
		var servedModel string
		for chunk, err := range s.binding.api.chatCompletionStream(ctx, modelName, contents, config) {
			if err != nil {
				yield(nil, err)
				return
			}

			if chunk == nil {
				yield(nil, fmt.Errorf("google: %w: nil speech chunk", speech.ErrInvalidResponse))
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
					yield(nil, fmt.Errorf("google: %w: audio after speech completion", speech.ErrInvalidResponse))
					return
				}
				if pending != nil && !yield(pending, nil) {
					return
				}
				pending = resp
			}
			if len(chunk.Candidates) > 0 && chunk.Candidates[0].FinishReason != "" {
				if err := validateProtocolCompletion(chunk); err != nil {
					yield(nil, fmt.Errorf("google: speech: %w: %w", speech.ErrInvalidResponse, err))
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
			yield(nil, fmt.Errorf("google: %w: speech stream returned no audio", speech.ErrInvalidResponse))
			return
		}
		yield(pending, nil)
	}
}
