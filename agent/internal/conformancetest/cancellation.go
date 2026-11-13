package conformancetest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

// CheckRestoreCancellation interrupts a built-in validator after each observed
// cancellation checkpoint. Sampling Err before canceling models cancellation
// arriving just after a successful check, without timing or scheduler races.
func CheckRestoreCancellation(t *testing.T, definition run.Definition, state run.ExecutionState) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	counter := &restoreCancellationContext{Context: ctx, cancel: cancel}
	_, err := definition.Restore(counter, state)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	checks := counter.checks.Load()
	if checks < 2 {
		t.Fatal("Restore has no cancellation checkpoint after entry")
	}
	for after := int64(1); after < checks; after++ {
		ctx, cancel := context.WithCancel(t.Context())
		interrupted := &restoreCancellationContext{Context: ctx, cancel: cancel, after: after}
		execution, err := definition.Restore(interrupted, state)
		cancel()
		if execution != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Restore canceled after check %d = %T, %v; want nil, context.Canceled", after, execution, err)
		}
	}
}

type restoreCancellationContext struct {
	context.Context
	cancel context.CancelFunc
	after  int64
	checks atomic.Int64
}

func (r *restoreCancellationContext) Err() error {
	err := r.Context.Err()
	if r.checks.Add(1) == r.after {
		r.cancel()
	}
	return err
}
