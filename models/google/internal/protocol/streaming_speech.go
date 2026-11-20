package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/speech"
)

var _ speech.Streamer = (*StreamingSpeechModel)(nil)
var _ speech.Model = (*StreamingSpeechModel)(nil)

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
	return speech.Collect(s.Stream(ctx, req), s.binding.maxResponseBytes)
}

func (s *StreamingSpeechModel) Stream(ctx context.Context, req *speech.Request) iter.Seq2[*speech.Response, error] {
	return func(yield func(*speech.Response, error) bool) {
		if err := req.Validate(); err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
			return
		}
		modelName, contents, config, err := s.prepare(req)
		if err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
			return
		}
		stream := speechStream{binding: s.binding}
		for chunk, err := range s.binding.api.chatCompletionStream(ctx, modelName, contents, config) {
			if err != nil {
				if errors.Is(err, ErrInvalidGenerationResponse) {
					err = fmt.Errorf("%w: %w", speech.ErrInvalidResponse, err)
				}
				yield(nil, err)
				return
			}
			released, err := stream.accept(chunk)
			if released != nil && !yield(released, nil) {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
		}
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		yield(stream.finish())
	}
}

func (s *StreamingSpeechModel) prepare(req *speech.Request) (string, []*genai.Content, *genai.GenerateContentConfig, error) {
	modelName, contents, config, err := s.binding.buildAPITTSRequest(req)
	if err != nil {
		return "", nil, nil, err
	}
	if modelName != ModelGemini31FlashTTSPreview {
		return "", nil, nil, fmt.Errorf("google: speech: model %q does not support streaming; use %q", modelName, ModelGemini31FlashTTSPreview)
	}
	return modelName, contents, config, nil
}

// speechStream folds one synthesis stream. It holds the latest audio chunk
// back, so terminal metadata travels with valid audio instead of an empty Core
// speech response.
type speechStream struct {
	binding    *speechBinding
	pending    *speech.Response
	format     speechAudioFormat
	generation generationStream
}

// accept folds chunk and returns the audio it releases. A chunk that fails
// after replacing the held audio still releases the previous one first, so the
// caller observes it before the error.
func (s *speechStream) accept(chunk *genai.GenerateContentResponse) (*speech.Response, error) {
	next, err := s.generation.advance(chunk)
	if err != nil {
		return nil, fmt.Errorf("google: speech: %w: %w", speech.ErrInvalidResponse, err)
	}
	var released *speech.Response
	audio, err := s.binding.buildTTSResponse(chunk, &s.format, maxGenerationEventBytes)
	switch {
	case err == nil:
		released, s.pending = s.pending, audio
	case !errors.Is(err, errNoAudio):
		return nil, err
	}
	if len(chunk.Candidates) > 0 && chunk.Candidates[0].FinishReason != "" {
		if err := validateProtocolCompletion(chunk); err != nil {
			return released, fmt.Errorf("google: speech: %w: %w", speech.ErrInvalidResponse, err)
		}
	}
	s.generation = next
	if s.pending == nil {
		return released, nil
	}
	s.pending.Metadata.Model = s.generation.modelVersion
	return released, s.pending.Metadata.Extra.Set(protocolKey(s.binding.provider, SpeechResponseExtensionSuffix), chunk)
}

func (s *speechStream) finish() (*speech.Response, error) {
	if !s.generation.finished() {
		return nil, fmt.Errorf("google: speech stream: %w", io.ErrUnexpectedEOF)
	}
	if s.pending == nil {
		return nil, fmt.Errorf("google: %w: speech stream returned no audio", speech.ErrInvalidResponse)
	}
	return s.pending, nil
}
