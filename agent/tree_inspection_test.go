package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type inspectionCommit struct {
	previous Digest
	next     TreeSnapshot
}

type inspectionDurability struct {
	*recordingTreeDurability
	effectKind     EffectBoundaryKind
	checkpointKind TreeCheckpointKind
	entered        chan inspectionCommit
	release        chan struct{}
	once           sync.Once
	failure        error
}

func (i *inspectionDurability) CommitEffect(ctx context.Context, boundary EffectBoundary) error {
	if err := i.recordingTreeDurability.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	if boundary.Kind() == i.effectKind {
		return i.block(inspectionCommit{previous: boundary.PreviousTreeDigest(), next: boundary.TreeSnapshot()})
	}
	return nil
}

func (i *inspectionDurability) CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error {
	if err := i.recordingTreeDurability.CommitCheckpoint(ctx, checkpoint); err != nil {
		return err
	}
	if checkpoint.Kind() == i.checkpointKind {
		return i.block(inspectionCommit{previous: checkpoint.PreviousTreeDigest(), next: checkpoint.TreeSnapshot()})
	}
	return nil
}

func (i *inspectionDurability) block(commit inspectionCommit) error {
	i.entered <- commit
	<-i.release
	return i.failure
}

func (i *inspectionDurability) unblock() { i.once.Do(func() { close(i.release) }) }

func TestInspectTreeDuringEveryRuntimeCommit(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		effect     EffectBoundaryKind
		checkpoint TreeCheckpointKind
		mode       string
	}{
		{name: "pending", effect: EffectBoundaryPending},
		{name: "settled", effect: EffectBoundarySettled},
		{name: "resolved", effect: EffectBoundaryResolved},
		{name: "child", checkpoint: TreeCheckpointChild, mode: "parent"},
		{name: "parked", checkpoint: TreeCheckpointParked, mode: "leaf_pause"},
		{name: "terminal", checkpoint: TreeCheckpointTerminal, mode: "leaf"},
		{name: "input", checkpoint: TreeCheckpointInput, mode: "leaf_pause"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			durability := &inspectionDurability{
				recordingTreeDurability: &recordingTreeDurability{},
				effectKind:              scenario.effect, checkpointKind: scenario.checkpoint,
				entered: make(chan inspectionCommit, 1), release: make(chan struct{}),
				failure: errors.New("inspection test lost acknowledgment"),
			}
			t.Cleanup(durability.unblock)
			engine, err := NewEngine(EngineConfig{TreeDurability: durability})
			if err != nil {
				t.Fatal(err)
			}
			var deployment Deployment
			var input Input
			if scenario.mode != "" {
				deployment = newChildTestDeployment(t)
				input, err = EncodeInput(childTestInput{Mode: scenario.mode})
			} else {
				var dispatcher Dispatcher = &engineTestDispatcher{policy: ReplayPolicyNever}
				if scenario.effect == EffectBoundaryResolved {
					dispatcher = &failingEngineTestDispatcher{}
				}
				deployment = engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
				input, err = EncodeInput(engineTestInput{Value: "inspect"})
			}
			if err != nil {
				t.Fatal(err)
			}
			root, err := engine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			var operation <-chan error
			if scenario.effect == EffectBoundaryResolved {
				snapshot := waitForUnknownSettlement(t, root)
				settlement, settlementErr := NewSettlement(snapshot.UnknownEffectIDs()[0], SettlementStatusSucceeded, []byte(`{"kind":"result","value":"resolved"}`))
				if settlementErr != nil {
					t.Fatal(settlementErr)
				}
				done := make(chan error, 1)
				operation = done
				go func() { done <- root.ResolveUnknownEffect(t.Context(), settlement) }()
			}
			if scenario.checkpoint == TreeCheckpointInput {
				waitForStatus(t, root, StatusPaused)
				id, _ := ParseSignalID("signal:inspection-input")
				request, requestErr := NewSignalRequest(id, WaitID{}, []byte(`{"value":"queued"}`))
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				done := make(chan error, 1)
				operation = done
				go func() {
					_, deliveryErr := root.DeliverSignals(t.Context(), request)
					done <- deliveryErr
				}()
			}
			commit := receiveTreeRuntimeProbe(t, durability.entered)
			checkpoints := len(durability.treeCheckpoints())
			effects := len(durability.effectBoundaries())
			var first ProcessSnapshot
			for range 64 {
				inspection := requireTreeInspection(t, engine, root.ID())
				if !inspection.CommitPending || inspection.Stopped || inspection.Freeze != TreeFreezeNone ||
					inspection.HeadDigest != commit.previous || !inspection.IncarnationID.Valid() {
					t.Fatalf("commit inspection=%+v", inspection)
				}
				if len(inspection.Processes) != 1 || inspection.Processes[0].Snapshot.ProcessID() != root.ID() {
					t.Fatal("inspection published a prospective child")
				}
				snapshot := inspection.Processes[0].Snapshot
				if first.Valid() && string(first.JSON()) != string(snapshot.JSON()) {
					t.Fatal("pure inspection changed the acknowledged Process")
				}
				first = snapshot
				inspection.Processes[0] = ProcessInspection{}
			}
			if len(durability.treeCheckpoints()) != checkpoints || len(durability.effectBoundaries()) != effects {
				t.Fatal("inspection produced a durability write")
			}
			if scenario.checkpoint == TreeCheckpointChild && len(commit.next.ProcessSnapshots()) != 2 {
				t.Fatal("child probe did not include a prospective child")
			}
			if scenario.effect == EffectBoundaryResolved && len(first.UnknownEffectIDs()) != 1 {
				t.Fatal("unacknowledged resolution removed the confirmed Unknown")
			}
			durability.unblock()
			if operation != nil && !errors.Is(receiveTreeRuntimeProbe(t, operation), durability.failure) {
				t.Fatal("operation did not report the lost acknowledgment")
			}
			_, awaitErr := root.Await(t.Context())
			if !errors.Is(awaitErr, durability.failure) {
				t.Fatalf("runtime outcome=%v", awaitErr)
			}
			for _, source := range []struct {
				name string
				err  error
			}{
				{name: "Await", err: awaitErr},
				{name: "stopped control", err: root.Pause(t.Context(), "inspect stopped runtime")},
			} {
				failure, ok := errors.AsType[*RuntimeError](source.err)
				if !ok || failure.ProcessID() != root.ID() || failure.HeadDigest() != commit.previous ||
					!errors.Is(failure, durability.failure) {
					t.Fatalf("%s runtime failure=%v", source.name, source.err)
				}
				*failure = RuntimeError{}
				_, nextErr := root.Await(t.Context())
				retained, ok := errors.AsType[*RuntimeError](nextErr)
				if !ok || retained.ProcessID() != root.ID() || retained.HeadDigest() != commit.previous ||
					!errors.Is(retained, durability.failure) {
					t.Fatalf("mutating %s error changed retained runtime failure: %v", source.name, nextErr)
				}
			}
			_, awaitErr = root.Await(t.Context())
			stopped := requireTreeInspection(t, engine, root.ID())
			if !stopped.Stopped || stopped.CommitPending || stopped.HeadDigest != commit.previous || len(stopped.Processes) != 1 {
				t.Fatalf("stopped inspection=%+v", stopped)
			}
			failure := stopped.Processes[0].RuntimeError
			if failure == nil || !errors.Is(failure, durability.failure) {
				t.Fatalf("inspection runtime failure=%v", failure)
			}
			*failure = RuntimeError{}
			if !errors.Is(awaitErr, durability.failure) {
				t.Fatal("mutating a report changed the retained Await error")
			}
			if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
				t.Fatal(closeErr)
			}
			afterClose := requireTreeInspection(t, engine, root.ID())
			if !errors.Is(afterClose.Processes[0].RuntimeError, durability.failure) {
				t.Fatal("mutating a report changed a later inspection")
			}
			if releaseErr := engine.ReleaseTree(t.Context(), root.ID()); releaseErr != nil {
				t.Fatal(releaseErr)
			}
			if _, inspectErr := engine.InspectTree(t.Context(), root.ID()); !errors.Is(inspectErr, ErrInvalidProcessRelation) {
				t.Fatalf("released inspection error=%v", inspectErr)
			}
		})
	}
}

func requireTreeInspection(t *testing.T, engine *Engine, rootID ProcessID) TreeInspection {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), treeRuntimeProgressTimeout)
	defer cancel()
	inspection, err := engine.InspectTree(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	return inspection
}
