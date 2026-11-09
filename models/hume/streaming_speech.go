package hume

import (
	"bufio"
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"sync"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/speech"
)

var _ speech.Streamer = (*StreamingSpeechModel)(nil)

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
			yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
			return
		}
		request, err := s.binding.buildAPIRequest(req)
		if err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
			return
		}
		if request.Utterances[0].Voice == nil && request.InstantMode == nil {
			instantMode := false
			request.InstantMode = &instantMode
		}
		if len(request.IncludeTimestampTypes) != 0 {
			yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("hume: speech stream: include_timestamp_types cannot be represented by Core audio-only stream responses")})
			return
		}
		body, err := s.binding.api.ttsStream(ctx, request)
		if err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err})
			return
		}
		closeBody := sync.OnceValue(body.Close)
		defer func() { _ = closeBody() }()

		lines := bufio.NewScanner(body)
		lines.Buffer(nil, httpclient.DefaultMaxJSONResponseBytes+len("\r\n"))
		lifecycle := generationStream{snippets: make(map[string]snippetProgress)}
		for lines.Scan() {
			raw := lines.Bytes()
			if len(raw) > httpclient.DefaultMaxJSONResponseBytes {
				yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: errors.New("hume: stream event exceeds JSON byte limit")})
				return
			}
			if len(bytes.TrimSpace(raw)) == 0 {
				continue
			}
			var event ttsStreamEvent
			if err := jsonv2.Unmarshal(raw, &event); err != nil {
				yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: fmt.Errorf("hume: decode streamed JSON response: %w", errors.Join(ctx.Err(), err, lines.Err(), closeBody()))})
				return
			}
			if event.Type != streamEventAudio {
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
		if err := errors.Join(ctx.Err(), lines.Err(), closeBody()); err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err})
			return
		}
		if err := lifecycle.complete(); err != nil {
			yield(nil, err)
		}
	}
}
