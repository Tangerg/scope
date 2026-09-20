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
	*recordingTreeCommitter
	effectKind     EffectBoundaryKind
	checkpointKind TreeCheckpointKind
	entered        chan inspectionCommit
	release        chan struct{}
	once           sync.Once
	failure        error
}

func (i *inspectionDurability) CommitEffect(ctx context.Context, boundary EffectBoundary) error {
	if err := i.recordingTreeCommitter.CommitEffect(ctx, boundary); err != nil {
		return err
	}
	if boundary.Kind() == i.effectKind {
		return i.block(inspectionCommit{previous: boundary.PreviousTreeDigest(), next: boundary.TreeSnapshot()})
	}
	return nil
}

func (i *inspectionDurability) CommitCheckpoint(ctx context.Context, checkpoint TreeCheckpoint) error {
	if err := i.recordingTreeCommitter.CommitCheckpoint(ctx, checkpoint); err != nil {
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
		{name: "pending", effect: EffectBoundaryKindPending},
		{name: "settled", effect: EffectBoundaryKindSettled},
		{name: "resolved", effect: EffectBoundaryKindResolved},
		{name: "child", checkpoint: TreeCheckpointKindChildStart, mode: "parent"},
		{name: "parked", checkpoint: TreeCheckpointKindParked, mode: "leaf_pause"},
		{name: "terminal", checkpoint: TreeCheckpointKindTerminal, mode: "leaf"},
		{name: "input", checkpoint: TreeCheckpointKindSignals, mode: "leaf_pause"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			committer := &inspectionDurability{
				recordingTreeCommitter: &recordingTreeCommitter{},
				effectKind:             scenario.effect, checkpointKind: scenario.checkpoint,
				entered: make(chan inspectionCommit, 1), release: make(chan struct{}),
				failure: errors.New("inspection test lost acknowledgment"),
			}
			t.Cleanup(committer.unblock)
			engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
			if err != nil {
				t.Fatal(err)
			}
			var deployment Deployment
			var input Payload
			if scenario.mode != "" {
				deployment = newChildTestDeployment(t)
				input, err = EncodePayload(childTestInput{Mode: scenario.mode})
			} else {
				var dispatcher Dispatcher = &engineTestDispatcher{policy: ReplayPolicyNever}
				if scenario.effect == EffectBoundaryKindResolved {
					dispatcher = &failingEngineTestDispatcher{}
				}
				deployment = engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
				input, err = EncodePayload(engineTestInput{Value: "inspect"})
			}
			if err != nil {
				t.Fatal(err)
			}
			root, err := engine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			var operation <-chan error
			if scenario.effect == EffectBoundaryKindResolved {
				snapshot := waitForUnknownSettlement(t, root)
				settlement, settlementErr := NewSettlement(snapshot.UnknownEffectIDs()[0], SettlementStatusSucceeded, []byte(`{"kind":"result","value":"resolved"}`))
				if settlementErr != nil {
					t.Fatal(settlementErr)
				}
				done := make(chan error, 1)
				operation = done
				go func() { done <- root.ResolveUnknownEffect(t.Context(), settlement) }()
			}
			if scenario.checkpoint == TreeCheckpointKindSignals {
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
			commit := receiveTreeRuntimeProbe(t, committer.entered)
			checkpoints := len(committer.treeCheckpoints())
			effects := len(committer.effectBoundaries())
			var first ProcessSnapshot
			for range 64 {
				inspection := requireTreeInspection(t, engine, root.ID())
				if !inspection.CommitPending || inspection.Stopped || inspection.Freeze != TreeFreezePhaseNone ||
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
			if len(committer.treeCheckpoints()) != checkpoints || len(committer.effectBoundaries()) != effects {
				t.Fatal("inspection produced a committer write")
			}
			if scenario.checkpoint == TreeCheckpointKindChildStart && len(commit.next.ProcessSnapshots()) != 2 {
				t.Fatal("child probe did not include a prospective child")
			}
			if scenario.effect == EffectBoundaryKindResolved && len(first.UnknownEffectIDs()) != 1 {
				t.Fatal("unacknowledged resolution removed the confirmed Unknown")
			}
			committer.unblock()
			if operation != nil && !errors.Is(receiveTreeRuntimeProbe(t, operation), committer.failure) {
				t.Fatal("operation did not report the lost acknowledgment")
			}
			_, awaitErr := root.Await(t.Context())
			if !errors.Is(awaitErr, committer.failure) {
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
					!errors.Is(failure, committer.failure) {
					t.Fatalf("%s runtime failure=%v", source.name, source.err)
				}
				*failure = RuntimeError{}
				_, nextErr := root.Await(t.Context())
				retained, ok := errors.AsType[*RuntimeError](nextErr)
				if !ok || retained.ProcessID() != root.ID() || retained.HeadDigest() != commit.previous ||
					!errors.Is(retained, committer.failure) {
					t.Fatalf("mutating %s error changed retained runtime failure: %v", source.name, nextErr)
				}
			}
			_, awaitErr = root.Await(t.Context())
			stopped := requireTreeInspection(t, engine, root.ID())
			if !stopped.Stopped || stopped.CommitPending || stopped.HeadDigest != commit.previous || len(stopped.Processes) != 1 {
				t.Fatalf("stopped inspection=%+v", stopped)
			}
			if scenario.checkpoint == TreeCheckpointKindChildStart {
				if err := root.Join(t.Context()); !errors.Is(err, committer.failure) {
					t.Fatalf("child rollback join error=%v", err)
				}
				assertNoPendingProcessStarts(t, engine)
				runtime := root.handle.runtime.Load()
				parent := runtime.processes[root.ID()]
				if parent.effectiveAllocations() != (resourceAmounts{}) || len(runtime.processes) != 1 {
					t.Fatal("rejected child checkpoint retained child budget or prospective Process")
				}
			}
			failure := stopped.Processes[0].RuntimeError
			if failure == nil || !errors.Is(failure, committer.failure) {
				t.Fatalf("inspection runtime failure=%v", failure)
			}
			*failure = RuntimeError{}
			if !errors.Is(awaitErr, committer.failure) {
				t.Fatal("mutating a report changed the retained Await error")
			}
			if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
				t.Fatal(closeErr)
			}
			afterClose := requireTreeInspection(t, engine, root.ID())
			if !errors.Is(afterClose.Processes[0].RuntimeError, committer.failure) {
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
