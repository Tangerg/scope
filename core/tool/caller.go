package tool

import (
	"bytes"
	"context"
	"fmt"
	"sync"
)

// Caller executes one admitted invocation. A completed result is not replaced
// by cancellation observed after execution; ordinary errors prove no outcome.
type Caller interface {
	Call(context.Context, Invocation) (Result, error)
}

type CallerFunc func(context.Context, Invocation) (Result, error)

func (c CallerFunc) Call(ctx context.Context, invocation Invocation) (Result, error) {
	return c(ctx, invocation)
}

// CallMiddleware may reject or synchronously forward an invocation once. It
// preserves invocation identity and cannot manufacture a continuation. A
// middleware that adds business effects needs a new Tool contract; transparent
// composition cannot inherit replay guarantees for additional external work.
// Bind invokes the factory for each call, so it must be concurrency-safe and
// leave the lifetime of shared resources with its constructor or host.
type CallMiddleware func(Caller) Caller

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
	if c.input == nil || !bytes.Equal(c.input.prompt, result.InputRequest.prompt) || !bytes.Equal(c.input.continuationState, result.InputRequest.continuationState) || !c.input.responseSchema.Equal(result.InputRequest.responseSchema) {
		return fmt.Errorf("%w: middleware cannot originate or replace an input request", ErrInvalidResult)
	}
	return nil
}
