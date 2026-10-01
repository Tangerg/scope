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
		for {
			var event ttsStreamEvent
			if err := jsonv2.UnmarshalDecode(decoder, &event); err != nil {
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
