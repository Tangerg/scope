package run

import (
	"context"
	"sync/atomic"
	"testing"
)

type cancelReceiptGate struct {
	*MemoryStore
	armed   atomic.Bool
	reached chan TreeSnapshot
	release chan struct{}
}

func (c *cancelReceiptGate) CommitCheckpoint(ctx context.Context, cut TreeCheckpoint) error {
	if err := c.MemoryStore.CommitCheckpoint(ctx, cut); err != nil {
		return err
	}
	if c.armed.CompareAndSwap(true, false) {
		c.reached <- cut.TreeSnapshot()
		<-c.release
	}
	return nil
}
func TestCancelAcknowledgesAuthoritativeCutRatherThanQueueAdmission(t *testing.T) {
	store := &cancelReceiptGate{MemoryStore: NewMemoryStore(), reached: make(chan TreeSnapshot, 1), release: make(chan struct{})}
	rt := controlValue(New(t.Context(), Config{Store: store}))
	p := engineTestDeployment(t, newEngineTestDefinition(t, "engine.wait", "wait"), nil)
	h := controlValue(rt.Start(t.Context(), p, StartRequest[Payload]{ID: StartID{Scope: "test", Key: "cancel"}, Input: controlValue(EncodePayload(engineTestInput{Value: "pause"}))}))
	waitForStatus(t, h, StatusWaiting)
	store.armed.Store(true)
	finished := make(chan error, 1)
	go func() { finished <- h.Cancel(t.Context(), "record cancellation") }()
	committed := <-store.reached
	select {
	case err := <-finished:
		t.Fatalf("Cancel returned before acknowledgment: %v", err)
	default:
	}
	snap := committed.ProcessSnapshots()[0]
	if snap.Status() != StatusCanceled && snap.state.PendingControl.Cancellation == nil {
		t.Fatal("acknowledged cut has neither cancellation intent nor terminal result")
	}
	close(store.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := h.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
