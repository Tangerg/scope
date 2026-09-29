package safeguard

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

// MiddlewareConfig controls one immutable Middleware. A zero Scope defaults
// to ScopeBoth. OnBlock runs synchronously before a rejection is returned.
type MiddlewareConfig struct {
	Scope   Scope
	OnBlock func(context.Context, Block)
}

type Middleware struct {
	matcher Matcher
	config  MiddlewareConfig
}

func NewMiddleware(matcher Matcher, config MiddlewareConfig) (*Middleware, error) {
	if lo.IsNil(matcher) {
		return nil, fmt.Errorf("%w: matcher is nil", ErrInvalidMiddlewareConfig)
	}
	if config.Scope == "" {
		config.Scope = ScopeBoth
	}
	if !config.Scope.Valid() {
		return nil, fmt.Errorf("%w: unknown scope %q", ErrInvalidMiddlewareConfig, config.Scope)
	}
	return &Middleware{matcher: matcher, config: config}, nil
}

func (m *Middleware) screen(ctx context.Context, scope Scope, text string) error {
	if !m.config.Scope.inspects(scope) || text == "" {
		return nil
	}
	match, err := m.matcher.Match(ctx, text)
	if err != nil {
		return fmt.Errorf("safeguard: match %s content: %w", scope, err)
	}
	if !match.Found {
		return nil
	}
	block := Block{Scope: scope, Term: match.Term}
	if m.config.OnBlock != nil {
		m.config.OnBlock(ctx, block)
	}
	return &UnsafeError{Block: block}
}

func (m *Middleware) screenInput(ctx context.Context, request *chat.Request) error {
	if request == nil {
		return nil
	}
	for index := range request.Messages {
		message := &request.Messages[index]
		if message.Role != chat.RoleSystem && message.Role != chat.RoleUser {
			continue
		}
		if err := m.screen(ctx, ScopeInput, message.Text()); err != nil {
			return err
		}
	}
	return nil
}

func (m *Middleware) screenOutput(ctx context.Context, response *chat.Response) error {
	if response == nil {
		return nil
	}
	return m.screen(ctx, ScopeOutput, response.Output.Text())
}

// Call is a [chat.CallMiddleware]. Input is screened before the model runs;
// output is screened before a response becomes visible to the caller.
func (m *Middleware) Call(next chat.Model) chat.Model {
	return chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
		if err := m.screenInput(ctx, request); err != nil {
			return nil, err
		}

		response, err := next.Call(ctx, request)
		if screeningErr := m.screenOutput(ctx, response); screeningErr != nil {
			return nil, errors.Join(err, screeningErr)
		}
		return response, err
	})
}

// Stream is a [chat.StreamMiddleware]. Output chunks are accumulated before
// screening so a term split across provider chunks is still detected. The
// chunk that completes an unsafe match is not yielded, and a chunk paired with
// a provider error is dropped because it was never screened.
func (m *Middleware) Stream(next chat.Streamer) chat.Streamer {
	return chat.StreamerFunc(func(ctx context.Context, request *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
		return func(yield func(*chat.ResponseDelta, error) bool) {
			if err := m.screenInput(ctx, request); err != nil {
				yield(nil, err)
				return
			}

			sequence := next.Stream(ctx, request)
			if sequence == nil {
				yield(nil, ErrNilStream)
				return
			}
			stream := safeguardStream{ctx: ctx, middleware: m, yield: yield}
			sequence(stream.consume)
		}
	})
}

type safeguardStream struct {
	ctx         context.Context
	middleware  *Middleware
	yield       func(*chat.ResponseDelta, error) bool
	accumulator chat.ResponseAccumulator
	stopped     bool
}

func (s *safeguardStream) consume(chunk *chat.ResponseDelta, streamErr error) bool {
	if s.stopped {
		return false
	}
	if streamErr != nil {
		return s.stop(streamErr)
	}
	if err := s.accumulator.Add(chunk); err != nil {
		return s.stop(fmt.Errorf("safeguard: accumulate stream: %w", err))
	}
	if err := s.middleware.screen(s.ctx, ScopeOutput, s.accumulator.Text()); err != nil {
		return s.stop(err)
	}
	if !s.yield(chunk, nil) {
		s.stopped = true
		return false
	}
	return true
}

func (s *safeguardStream) stop(err error) bool {
	s.stopped = true
	s.yield(nil, err)
	return false
}
