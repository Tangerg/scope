package run

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"strings"
	"testing"
)

func TestRejectedControlRequestsPreserveTheCommittedWaitingTree(t *testing.T) {
	engine := controlValue(New(t.Context(), Config{Store: NewMemoryStore()}))
	root := controlValue(engine.Start(t.Context(), newChildTestDeployment(t), StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: controlValue(EncodePayload(childTestInput{Mode: "external_wait"}))}))
	t.Cleanup(func() {
		finishExternalWaitRestoreProcess(t, root)
		mustCloseEngine(t, engine)
	})
	waitForStatus(t, root, StatusWaiting)
	before := controlValue(engine.CaptureTree(t.Context(), root.Relation().ProcessID()))
	if root.TreeIncarnationID() != before.IncarnationID() {
		t.Fatal("handle and durable head disagree about the owning incarnation")
	}
	if accepted, err := root.DeliverSignals(t.Context()); accepted || !errors.Is(err, ErrInvalidSignalRequest) {
		t.Fatalf("empty delivery admitted: %t, %v", accepted, err)
	}
	for _, reason := range []string{"", " untrimmed ", strings.Repeat("x", maxTerminationReasonBytes+1)} {
		if err := root.Cancel(t.Context(), reason); !errors.Is(err, ErrInvalidProcessControl) {
			t.Fatalf("invalid cancellation reason admitted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := root.Await(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result wait: %v", err)
	}
	if err := root.Join(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled drain wait: %v", err)
	}
	if err := root.Pause(ctx, "caller already left"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled pause admitted: %v", err)
	}
	if err := root.Cancel(ctx, "caller already left"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled termination admitted: %v", err)
	}
	after := controlValue(engine.CaptureTree(t.Context(), root.Relation().ProcessID()))
	if before.Digest() != after.Digest() {
		t.Fatal("rejected control request changed the committed wait")
	}
}
