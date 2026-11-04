package interaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// DefaultMaxStreamDeltaBytes keeps stream admission bounded independently of
// the final settlement size, including streams that never finish.
const DefaultMaxStreamDeltaBytes = 64 << 20

// ModelSource is how an Interaction obtains each model response. CallModel and
// StreamModel are its only forms, so a configuration names exactly one way to
// reach the model and carries only the settings that way uses.
type ModelSource interface {
	respond(ctx context.Context, request *chat.Request, emit run.DeltaEmitter, accumulator *chat.ResponseAccumulator) (*chat.Response, error)
	validate() error
	// operation names the callback for a recovered panic.
	operation() string
	// streamLimit is zero for a source that does not stream.
	streamLimit() int
}

type callModel struct{ model chat.Model }

// CallModel obtains each response from one complete model call.
func CallModel(model chat.Model) ModelSource { return callModel{model: model} }

func (c callModel) respond(ctx context.Context, request *chat.Request, _ run.DeltaEmitter, _ *chat.ResponseAccumulator) (*chat.Response, error) {
	return c.model.Call(ctx, request)
}

func (c callModel) validate() error {
	if lo.IsNil(c.model) {
		return fmt.Errorf("%w: model is nil", ErrInvalidDispatcherConfig)
	}
	return nil
}

func (callModel) operation() string { return "chat.Model.Call" }
func (callModel) streamLimit() int  { return 0 }

type StreamModelConfig struct {
	Streamer chat.Streamer
	// MaxDeltaBytes bounds cumulative Scope-encoded ModelResponseDelta payloads
	// before accumulation, independently of MaxResponseBytes. Zero uses
	// DefaultMaxStreamDeltaBytes.
	MaxDeltaBytes int
}

type streamModel struct {
	streamer      chat.Streamer
	maxDeltaBytes int
}

// StreamModel obtains each response by accumulating a stream whose deltas the
// Runtime publishes as they arrive.
func StreamModel(config StreamModelConfig) ModelSource {
	limit := config.MaxDeltaBytes
	if limit == 0 {
		limit = DefaultMaxStreamDeltaBytes
	}
	return streamModel{streamer: config.Streamer, maxDeltaBytes: limit}
}

func (s streamModel) validate() error {
	if lo.IsNil(s.streamer) {
		return fmt.Errorf("%w: streamer is nil", ErrInvalidDispatcherConfig)
	}
	if s.maxDeltaBytes < 0 {
		return fmt.Errorf("%w: MaxDeltaBytes must not be negative", ErrInvalidDispatcherConfig)
	}
	return nil
}

func (streamModel) operation() string  { return "chat.Streamer.Stream" }
func (s streamModel) streamLimit() int { return s.maxDeltaBytes }

func (s streamModel) respond(ctx context.Context, request *chat.Request, emit run.DeltaEmitter, accumulator *chat.ResponseAccumulator) (*chat.Response, error) {
	sequence := s.streamer.Stream(ctx, request)
	if sequence == nil {
		return nil, chat.ErrNilStream
	}
	remainingDeltaBytes := s.maxDeltaBytes
	for delta, err := range sequence {
		if err != nil {
			return nil, err
		}
		if delta == nil {
			return nil, errors.New("model stream yielded a nil response Delta")
		}
		payload, err := encodeModelResponseDelta(delta)
		if err != nil {
			return nil, err
		}
		if len(payload) > remainingDeltaBytes {
			return nil, ErrModelStreamTooLarge
		}
		remainingDeltaBytes -= len(payload)
		if err := accumulator.Add(delta); err != nil {
			return nil, fmt.Errorf("accumulate model stream: %w", err)
		}
		if emit != nil {
			emit(payload)
		}
	}
	// Response rejects a stream that produced no deltas, so no local tally is kept.
	response, err := accumulator.Response()
	if err != nil {
		return nil, fmt.Errorf("complete model stream: %w", err)
	}
	return response, nil
}
