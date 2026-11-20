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
		request, err := s.prepare(req)
		if err != nil {
			yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
			return
		}
		body, err := s.binding.api.ttsStream(ctx, *request)
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
			event, err := decodeStreamEvent(lines.Bytes())
			if err != nil {
				yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: errors.Join(err, ctx.Err(), lines.Err(), closeBody())})
				return
			}
			if event == nil {
				continue
			}
			response, err := lifecycle.respond(event, request.Version)
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

func (s *StreamingSpeechModel) prepare(req *speech.Request) (*ttsRequest, error) {
	request, err := s.binding.buildAPIRequest(req)
	if err != nil {
		return nil, err
	}
	if request.Utterances[0].Voice == nil && request.InstantMode == nil {
		request.InstantMode = new(false)
	}
	if len(request.IncludeTimestampTypes) != 0 {
		return nil, errors.New("hume: speech stream: include_timestamp_types cannot be represented by Core audio-only stream responses")
	}
	return request, nil
}

// decodeStreamEvent returns nil for a blank line between events.
func decodeStreamEvent(raw []byte) (*ttsStreamEvent, error) {
	if len(raw) > httpclient.DefaultMaxJSONResponseBytes {
		return nil, errors.New("hume: stream event exceeds JSON byte limit")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	event := new(ttsStreamEvent)
	if err := jsonv2.Unmarshal(raw, event); err != nil {
		return nil, fmt.Errorf("hume: decode streamed JSON response: %w", err)
	}
	return event, nil
}
