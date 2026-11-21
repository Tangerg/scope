package tool

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/samber/lo"
)

// CallMiddleware may reject or synchronously forward an invocation once. It
// preserves invocation identity and cannot manufacture a continuation. A
// middleware that adds business effects needs a new Tool contract; transparent
// composition cannot inherit replay guarantees for additional external work.
// Wrap invokes the factory for each call, so it must be concurrency-safe and
// leave the lifetime of shared resources with its constructor or host.
type CallMiddleware func(next Caller) Caller

// Wrap decorates the calls of t. The first middleware is outermost and nil
// entries are skipped, as in chat.Wrap. The result unwraps to t, so every
// declaration t makes stays discoverable through As; with no middleware, or a
// nil t, Wrap returns t itself.
func Wrap(t Tool, middlewares ...CallMiddleware) Tool {
	middlewares = slices.DeleteFunc(slices.Clone(middlewares), func(middleware CallMiddleware) bool { return middleware == nil })
	if len(middlewares) == 0 || lo.IsNil(t) {
		return t
	}
	return &wrappedTool{tool: t, middlewares: middlewares}
}

// wrappedTool forwards Definition to the Tool it decorates rather than caching
// it: Bind reads the definition once, and a second copy could disagree.
type wrappedTool struct {
	tool        Tool
	middlewares []CallMiddleware
}

func (w *wrappedTool) Definition() Definition { return w.tool.Definition() }

func (w *wrappedTool) Unwrap() Tool { return w.tool }

func (w *wrappedTool) Call(ctx context.Context, invocation Invocation) (Result, error) {
	return w.callFrom(ctx, invocation, 0)
}

// callFrom gives each middleware a forward valid for this call alone, so a
// middleware can neither repeat nor outlive the execution it was admitted for.
func (w *wrappedTool) callFrom(ctx context.Context, invocation Invocation, index int) (Result, error) {
	if index == len(w.middlewares) {
		return w.tool.Call(ctx, invocation)
	}
	forward := &callContinuation{invocation: invocation, next: CallerFunc(func(ctx context.Context, invocation Invocation) (Result, error) {
		return w.callFrom(ctx, invocation, index+1)
	})}
	defer forward.close()
	next := w.middlewares[index](forward)
	if lo.IsNil(next) {
		return Result{}, fmt.Errorf("%w: middleware[%d] returned nil", ErrInvalidTool, index)
	}
	result, err := next.Call(ctx, invocation)
	if validationErr := forward.validate(result); validationErr != nil {
		return Result{}, validationErr
	}
	return result, err
}

// forwardPhase is the single forward a middleware may make.
type forwardPhase uint8

const (
	forwardPending forwardPhase = iota
	forwardRunning
	forwardReturned
)

type callContinuation struct {
	mu         sync.Mutex
	invocation Invocation
	next       Caller
	phase      forwardPhase
	// closed records that the middleware returned; a forward it started may
	// still be running.
	closed    bool
	input     *InputRequest
	violation error
}

func (c *callContinuation) Call(ctx context.Context, invocation Invocation) (Result, error) {
	c.mu.Lock()
	if c.closed || c.phase != forwardPending || invocation.provenance != c.invocation.provenance || invocation.contract != c.invocation.contract {
		c.violation = fmt.Errorf("%w: middleware must forward the admitted invocation at most once", ErrInvalidInvocation)
		err := c.violation
		c.mu.Unlock()
		return Result{}, err
	}
	c.phase = forwardRunning
	c.mu.Unlock()
	result, err := c.next.Call(ctx, invocation)
	c.mu.Lock()
	c.phase = forwardReturned
	if err == nil && result.InputRequest != nil {
		c.input = new(*result.InputRequest)
	}
	c.mu.Unlock()
	return result, err
}

func (c *callContinuation) close() { c.mu.Lock(); c.closed = true; c.mu.Unlock() }

func (c *callContinuation) validate(result Result) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.violation != nil {
		return c.violation
	}
	if c.phase == forwardRunning {
		return fmt.Errorf("%w: middleware returned before its forwarded call", ErrInvalidResult)
	}
	if result.InputRequest == nil {
		if c.input != nil {
			return fmt.Errorf("%w: middleware cannot complete an input request", ErrInvalidResult)
		}
		return nil
	}
	if c.input == nil || !c.input.Equal(*result.InputRequest) {
		return fmt.Errorf("%w: middleware cannot originate or replace an input request", ErrInvalidResult)
	}
	return nil
}
