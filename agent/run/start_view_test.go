package run

import (
	"context"
	"errors"
	"testing"
)

func TestStartedTreeReadNeverRegistersOrActivatesAWriter(t *testing.T) {
	store := NewMemoryStore()
	leader := controlValue(New(t.Context(), Config{Store: store}))
	reader := controlValue(New(t.Context(), Config{Store: store}))
	views := controlValue(reader.StartedTrees())
	id := StartID{Scope: "tenant-a", Key: "wire-id"}
	p := controlValue(leader.Start(t.Context(), newChildTestDeployment(t), StartRequest[Payload]{ID: id, Input: controlValue(EncodePayload(childTestInput{Mode: "leaf"}))}))
	_ = mustAwait(t, p)
	head, found, err := views.Load(t.Context(), id)
	if err != nil || !found || head.RootID() != p.ID() || head.IncarnationID() != p.TreeIncarnationID() {
		t.Fatal("unrecognized durable activation", err)
	}
	if _, found := reader.Process(p.ID()); found {
		t.Fatal("read installed a writer")
	}
	if _, found, err := views.Load(t.Context(), StartID{Scope: "tenant-b", Key: id.Key}); err != nil || found {
		t.Fatal("foreign scope could read the head", err)
	}
	mustCloseEngine(t, leader)
	mustCloseEngine(t, reader)
}

func TestRepeatedStartReturnsOnlyTheAcknowledgedWriter(t *testing.T) {
	ctx := t.Context()
	store := NewMemoryStore()
	previous := controlValue(New(ctx, Config{Store: store}))
	current := controlValue(New(ctx, Config{Store: store}))
	program := newChildTestDeployment(t)
	req := StartRequest[Payload]{ID: StartID{Scope: "test", Key: "handover"}, Input: controlValue(EncodePayload(childTestInput{Mode: "external_wait"}))}
	original := controlValue(previous.Start(ctx, program, req))
	waitForStatus(t, original, StatusWaiting)
	views := controlValue(previous.StartedTrees())
	head, found, err := views.Load(ctx, req.ID)
	if err != nil || !found {
		t.Fatal("load accepted start", err)
	}
	recovered := controlValue(current.Recover(ctx, program, head))
	stale, err := previous.Start(ctx, program, req)
	receipt, accepted := errors.AsType[*StartAcceptedError](err)
	if stale != nil || !accepted || receipt.Receipt.RootID != recovered.ID() || receipt.Receipt.ID != req.ID {
		t.Errorf("retry at superseded writer: handle=%v error=%v", stale != nil, err)
	}
	local := controlValue(current.Start(ctx, program, req))
	if local.TreeIncarnationID() != recovered.TreeIncarnationID() || local.TreeIncarnationID() == original.TreeIncarnationID() {
		t.Fatal("retry at current writer returned a different activation")
	}
	if err := recovered.Answer(ctx, head.Pending()[0].WaitID, "approved"); err != nil {
		t.Fatal(err)
	}
	if result := mustAwait(t, recovered); result.Termination().Status() != StatusCompleted {
		t.Fatal(result.Termination())
	}
	if err := recovered.Join(ctx); err != nil {
		t.Fatal(err)
	}
	// Submitting a command lets the fenced activation drain before closing it.
	if err := original.Cancel(ctx, "writer replaced"); err != nil && !errors.Is(err, ErrTreeIncarnationConflict) {
		t.Fatal(err)
	}
	if err := original.Join(ctx); !errors.Is(err, ErrTreeIncarnationConflict) {
		t.Fatal("superseded writer was not fenced", err)
	}
	mustCloseEngine(t, previous)
	mustCloseEngine(t, current)
}

func TestRepeatedStartWithUnconfirmedWriterReturnsAcceptance(t *testing.T) {
	base := NewMemoryStore()
	fault := errors.New("head unavailable")
	for _, tc := range []struct {
		name  string
		store Store
		cause error
	}{
		{name: "no reader", store: struct{ Store }{NewMemoryStore()}},
		{name: "read failure", store: evidenceReadStore{Store: base, acceptance: base.LookupStart, head: func(context.Context, ProcessID) (TreeSnapshot, bool, error) {
			return TreeSnapshot{}, false, fault
		}}, cause: fault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			rt := controlValue(New(ctx, Config{Store: tc.store}))
			program := newChildTestDeployment(t)
			req := StartRequest[Payload]{ID: StartID{Scope: "test", Key: "unreadable"}, Input: controlValue(EncodePayload(childTestInput{Mode: "leaf"}))}
			original := controlValue(rt.Start(ctx, program, req))
			if err := original.Join(ctx); err != nil {
				t.Fatal(err)
			}
			duplicate, err := rt.Start(ctx, program, req)
			receipt, accepted := errors.AsType[*StartAcceptedError](err)
			if duplicate != nil || !accepted || receipt.Receipt.RootID != original.ID() || receipt.Receipt.ID != req.ID {
				t.Fatalf("retry without a confirmed head: handle=%v error=%v", duplicate != nil, err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("retry dropped the storage error: %v", err)
			}
			mustCloseEngine(t, rt)
		})
	}
}
