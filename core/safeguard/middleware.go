package safeguard

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/Tangerg/scope/core/invocation"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

// MiddlewareConfig controls one immutable Middleware. Matcher is required.
// OnBlock runs synchronously before a rejection is returned.
type MiddlewareConfig struct {
	Matcher Matcher
	Scope   Scope
	OnBlock func(context.Context, Block)
}

type Middleware struct {
	config MiddlewareConfig
}

func NewMiddleware(config MiddlewareConfig) (*Middleware, error) {
	if lo.IsNil(config.Matcher) {
		return nil, fmt.Errorf("%w: matcher is nil", ErrInvalidMiddlewareConfig)
	}
	if !config.Scope.Valid() {
		return nil, fmt.Errorf("%w: unknown scope %q", ErrInvalidMiddlewareConfig, config.Scope)
	}
	return &Middleware{config: config}, nil
}

func (m *Middleware) screen(ctx context.Context, direction Direction, text string) error {
	if !m.config.Scope.inspects(direction) || text == "" {
		return nil
	}
	match, err := m.config.Matcher.Match(ctx, text)
	return m.decision(ctx, direction, match, err)
}

func (m *Middleware) decision(ctx context.Context, direction Direction, match Match, err error) error {
	if err != nil {
		return fmt.Errorf("safeguard: match %s content: %w", direction, err)
	}
	if !match.Found {
		return nil
	}
	block := Block{Direction: direction, Term: match.Term}
	if m.config.OnBlock != nil {
		m.config.OnBlock(ctx, block)
	}
	return &UnsafeError{Block: block}
}

func (m *Middleware) screenInput(ctx context.Context, request *chat.Request) error {
	if request == nil || !m.config.Scope.inspects(DirectionInput) {
		return nil
	}
	for index := range request.Messages {
		message := &request.Messages[index]
		if message.Role != chat.RoleSystem && message.Role != chat.RoleUser {
			continue
		}
		if err := m.screen(ctx, DirectionInput, message.Text()); err != nil {
			return err
		}
	}
	return nil
}

func (m *Middleware) screenOutput(ctx context.Context, response *chat.Response) error {
	if response == nil || !m.config.Scope.inspects(DirectionOutput) {
		return nil
	}
	return m.screen(ctx, DirectionOutput, response.Output.Text())
}

// Call is a [chat.CallMiddleware]. Input is screened before the model runs;
// output is screened before a response becomes visible to the caller.
func (m *Middleware) Call(next chat.Model) chat.Model {
	return chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
		if err := m.screenInput(ctx, request); err != nil {
			return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
		}

		response, err := next.Call(ctx, request)
		if screeningErr := m.screenOutput(ctx, response); screeningErr != nil {
			return nil, m.outputFailure(err, screeningErr)
		}
		return response, err
	})
}

func (m *Middleware) outputFailure(cause, screeningErr error) error {
	kind := invocation.FailureFailed
	if cause != nil && invocation.ClassifyFailure(cause) != invocation.FailureFailed {
		kind = invocation.FailureUnknown
	}
	return &invocation.Failure{Kind: kind, Cause: errors.Join(cause, screeningErr)}
}

// Stream is a [chat.StreamMiddleware]. Incremental matchers screen each fragment
// before yielding its chunk. Other matchers screen the complete buffered output
// once. A completed, safe generation survives a later provider error; incomplete
// or rejected output is discarded. A chunk paired with an error is never exposed.
func (m *Middleware) Stream(next chat.Streamer) chat.Streamer {
	return chat.StreamerFunc(func(ctx context.Context, request *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			if err := m.screenInput(ctx, request); err != nil {
				yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err})
				return
			}

			stream := safeguardStream{ctx: ctx, middleware: m, yield: yield}
			if matcher, ok := m.config.Matcher.(StreamMatcher); ok && m.config.Scope.inspects(DirectionOutput) {
				stream.scan = matcher.NewScan()
				if lo.IsNil(stream.scan) {
					yield(nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: fmt.Errorf("%w: matcher returned a nil scan", ErrInvalidMiddlewareConfig)})
					return
				}
			}
			sequence := next.Stream(ctx, request)
			if sequence == nil {
				yield(nil, chat.ErrNilStream)
				return
			}
			if !m.config.Scope.inspects(DirectionOutput) {
				sequence(yield)
				return
			}
			sequence(stream.consume)
			stream.finish(nil)
		}
	})
}

type safeguardStream struct {
	ctx         context.Context
	middleware  *Middleware
	yield       func(*chat.ResponseDelta, error) bool
	accumulator chat.ResponseAccumulator
	scan        Scan
	buffer      []*chat.ResponseDelta
	stopped     bool
}

func (s *safeguardStream) consume(chunk *chat.ResponseDelta, streamErr error) bool {
	if s.stopped {
		return false
	}
	if streamErr != nil {
		if s.scan == nil {
			s.finish(streamErr)
			return false
		}
		return s.stop(streamErr)
	}
	if err := s.accumulator.Add(chunk); err != nil {
		return s.stop(fmt.Errorf("safeguard: accumulate stream: %w", err))
	}
	if s.scan == nil {
		s.buffer = append(s.buffer, chunk.Clone())
		return true
	}
	match, matchErr := s.scan.Add(s.ctx, chunk.Text())
	if screeningErr := s.middleware.decision(s.ctx, DirectionOutput, match, matchErr); screeningErr != nil {
		return s.stop(s.middleware.outputFailure(nil, screeningErr))
	}
	if !s.yield(chunk, nil) {
		s.stopped = true
		return false
	}
	return true
}

func (s *safeguardStream) finish(streamErr error) {
	if s.stopped {
		return
	}
	response, completionErr := s.accumulator.Response()
	if completionErr != nil {
		if streamErr == nil {
			streamErr = fmt.Errorf("safeguard: complete stream: %w", completionErr)
		}
		s.stop(streamErr)
		return
	}
	if s.scan != nil {
		return
	}
	if screeningErr := s.middleware.screenOutput(s.ctx, response); screeningErr != nil {
		s.stop(s.middleware.outputFailure(streamErr, screeningErr))
		return
	}
	for _, chunk := range s.buffer {
		if !s.yield(chunk, nil) {
			s.stopped = true
			return
		}
	}
	if streamErr != nil {
		s.stop(streamErr)
		return
	}
	s.stopped = true
}

func (s *safeguardStream) stop(err error) bool {
	s.stopped = true
	s.yield(nil, err)
	return false
}
