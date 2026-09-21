package protocol

import (
	"context"
	"errors"
	"fmt"
	"iter"

	tts "github.com/Tangerg/scope/core/speech"
)

var _ tts.Streamer = (*StreamingAudioTTSModel)(nil)

// StreamingAudioTTSModel exposes the native incremental audio protocol.
type StreamingAudioTTSModel struct{ binding *speechBinding }

func NewStreamingAudioTTSModel(ctx context.Context, config AudioTTSModelConfig) (*StreamingAudioTTSModel, error) {
	binding, err := newSpeechBinding(ctx, config)
	if err != nil {
		return nil, err
	}
	if config.DefaultOptions.Model != ModelGemini31FlashTTSPreview {
		return nil, fmt.Errorf("google: streaming speech requires model %q", ModelGemini31FlashTTSPreview)
	}
	return &StreamingAudioTTSModel{binding: binding}, nil
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

		for chunk, err := range s.binding.api.chatCompletionStream(ctx, modelName, contents, config) {
			if err != nil {
				yield(nil, err)
				return
			}

			resp, err := s.binding.buildTTSResponse(chunk)
			if err != nil {
				// Skip chunks that don't carry audio (Gemini may emit
				// metadata-only chunks during streaming) rather than
				// fail the whole stream.
				if errors.Is(err, errNoAudio) {
					continue
				}
				yield(nil, err)
				return
			}
			if !yield(resp, nil) {
				return
			}
		}
	}
}
