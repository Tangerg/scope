package hume

import (
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/Tangerg/scope/core/speech"
)

var _ speech.Streamer = (*StreamingSpeechModel)(nil)

// StreamingSpeechModel exposes the native incremental audio protocol.
type StreamingSpeechModel struct{ binding *speechBinding }

func NewStreamingSpeechModel(ctx context.Context, config SpeechModelConfig) (*StreamingSpeechModel, error) {
	binding, err := newSpeechBinding(ctx, config)
	if err != nil {
		return nil, err
	}
	return &StreamingSpeechModel{binding: binding}, nil
}

func (s *StreamingSpeechModel) Stream(ctx context.Context, req *speech.Request) iter.Seq2[*speech.Response, error] {
	return func(yield func(*speech.Response, error) bool) {
		if err := req.Validate(); err != nil {
			yield(nil, err)
			return
		}
		request, err := s.binding.buildAPIRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		if request.Utterances[0].Voice == nil && request.InstantMode == nil {
			instantMode := false
			request.InstantMode = &instantMode
		}
		if len(request.IncludeTimestampTypes) != 0 {
			yield(nil, errors.New("hume: speech stream: include_timestamp_types cannot be represented by Core audio-only stream responses"))
			return
		}
		body, err := s.binding.api.ttsStream(ctx, request)
		if err != nil {
			yield(nil, err)
			return
		}
		defer body.Close()

		decoder := jsontext.NewDecoder(body)
		lifecycle := snippetStream{snippets: make(map[string]snippetProgress)}
		for {
			var event ttsStreamEvent
			if err := jsonv2.UnmarshalDecode(decoder, &event); err != nil {
				if errors.Is(err, io.EOF) {
					if contextErr := ctx.Err(); contextErr != nil {
						yield(nil, contextErr)
					} else if completionErr := lifecycle.complete(); completionErr != nil {
						yield(nil, completionErr)
					}
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
			if err := lifecycle.accept(event); err != nil {
				yield(nil, err)
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

type snippetProgress struct {
	generation string
	request    string
	next       int64
	finished   bool
}

type snippetStream struct{ snippets map[string]snippetProgress }

func (s *snippetStream) accept(event ttsStreamEvent) error {
	if event.SnippetID == "" || event.GenerationID == "" || event.RequestID == "" {
		return errors.New("hume: streamed audio is missing snippet identity")
	}
	progress, exists := s.snippets[event.SnippetID]
	if !exists {
		progress.generation, progress.request = event.GenerationID, event.RequestID
	}
	if progress.finished || progress.generation != event.GenerationID || progress.request != event.RequestID || event.ChunkIndex != progress.next {
		return errors.New("hume: streamed snippet has inconsistent identity, order or completion")
	}
	progress.next++
	progress.finished = event.IsLastChunk
	s.snippets[event.SnippetID] = progress
	return nil
}

// complete reports whether the stream delivered a well-formed generation. A clean
// EOF is success only when at least one snippet arrived and every started snippet
// reached its final chunk; an empty body carries no completion evidence and fails.
func (s *snippetStream) complete() error {
	if len(s.snippets) == 0 {
		return errors.New("hume: stream ended before any audio snippet arrived")
	}
	for _, progress := range s.snippets {
		if !progress.finished {
			return errors.New("hume: stream ended before every snippet completed")
		}
	}
	return nil
}
