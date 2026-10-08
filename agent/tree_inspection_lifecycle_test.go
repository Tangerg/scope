package agent

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
)

func TestInspectTreeRemainsAvailableWhileFreezeOperationIsHeld(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever, started: make(chan struct{}, 1), block: release}
	deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(engineTestInput{Value: "frozen"})
	root, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	receiveTreeRuntimeProbe(t, dispatcher.started)
	ctx, cancel := context.WithTimeout(t.Context(), treeRuntimeProgressTimeout)
	defer cancel()
	operation, err := engine.acquireTreeOperation(ctx, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer operation.release()
	owner, err := engine.runtimeForTree(root.ID())
	if err != nil {
		t.Fatal(err)
	}
	frozen := make(chan treeFreezeAcquisitionResult, 1)
	go func() {
		freeze, snapshot, freezeErr := owner.acquireTreeFreeze(ctx)
		frozen <- treeFreezeAcquisitionResult{freeze: freeze, snapshot: snapshot, err: freezeErr}
	}()
	for {
		inspection, inspectErr := engine.InspectTree(ctx, root.ID())
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if inspection.Freeze == TreeFreezePhaseAcquiring {
			if inspection.Processes[0].Work != ProcessWorkDispatch || !inspection.Processes[0].EffectID.Valid() {
				t.Fatalf("acquiring freeze work=%+v", inspection.Processes[0])
			}
			break
		}
		runtime.Gosched()
	}
	unblock()
	acquired := receiveTreeRuntimeProbe(t, frozen)
	if acquired.err != nil {
		t.Fatal(acquired.err)
	}
	held := requireTreeInspection(t, engine, root.ID())
	if held.Freeze != TreeFreezePhaseHeld || held.CommitPending || held.Processes[0].Work != ProcessWorkQueued {
		t.Fatalf("held freeze inspection=%+v", held)
	}
	if releaseErr := acquired.freeze.release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	operation.release()
	if result, awaitErr := root.Await(ctx); awaitErr != nil || result.Termination().Status() != StatusCompleted {
		t.Fatalf("unfrozen result=%s error=%v", result.Termination().Status(), awaitErr)
	}
	if releaseErr := engine.ReleaseTree(ctx, root.ID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	mustCloseEngine(t, engine)
}

func TestConcurrentInspectionsPreserveCompletionAndRelease(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever, started: make(chan struct{}, 1), block: release}
	deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
	engine, err := NewEngine(EngineConfig{TreeCommitter: &recordingTreeCommitter{}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(engineTestInput{Value: "flood"})
	root, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), treeRuntimeProgressTimeout)
	defer cancel()
	const (
		readers              = 4
		inspectionsPerReader = 64
	)
	started := make(chan struct{}, readers)
	finished := make(chan error, readers)
	continueInspections := make(chan struct{})
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		unblock()
		workers.Wait()
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(t.Context()), treeRuntimeProgressTimeout)
		defer cancelCleanup()
		if joinErr := root.Join(cleanupCtx); joinErr != nil {
			t.Error(joinErr)
		}
		if closeErr := engine.Close(cleanupCtx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	receiveTreeRuntimeProbe(t, dispatcher.started)
	for range readers {
		workers.Go(func() {
			// Bounded callers exercise API races; owner-turn tests keep lanes full
			// without depending on Go's scheduling of an unlimited query loop.
			for index := range inspectionsPerReader {
				inspection, inspectErr := engine.InspectTree(ctx, root.ID())
				if errors.Is(inspectErr, ErrTreeNotFound) {
					finished <- nil
					return
				}
				if inspectErr != nil {
					finished <- inspectErr
					return
				}
				if inspection.RootID != root.ID() || len(inspection.Processes) != 1 ||
					inspection.Processes[0].Snapshot.ProcessID() != root.ID() {
					finished <- errors.New("inspection lost the published root")
					return
				}
				inspection.Processes[0] = ProcessInspection{}
				if index == 0 {
					started <- struct{}{}
					select {
					case <-continueInspections:
					case <-ctx.Done():
						finished <- ctx.Err()
						return
					}
				}
			}
			finished <- nil
		})
	}
	for range readers {
		receiveTreeRuntimeProbe(t, started)
	}
	close(continueInspections)
	unblock()
	if result, awaitErr := root.Await(ctx); awaitErr != nil || result.Termination().Status() != StatusCompleted {
		t.Fatalf("queries delayed completion: status=%s error=%v", result.Termination().Status(), awaitErr)
	}
	if releaseErr := engine.ReleaseTree(ctx, root.ID()); releaseErr != nil {
		t.Fatalf("queries delayed release: %v", releaseErr)
	}
	if _, inspectErr := engine.InspectTree(ctx, root.ID()); !errors.Is(inspectErr, ErrTreeNotFound) {
		t.Fatalf("released tree inspection error=%v", inspectErr)
	}
	for range readers {
		if readerErr := receiveTreeRuntimeProbe(t, finished); readerErr != nil {
			t.Fatal(readerErr)
		}
	}
	workers.Wait()
	mustCloseEngine(t, engine)
}

func TestInspectTreeCancellationAndBoundedAdmission(t *testing.T) {
	owner := newTreeRuntime(&Engine{}, ProcessID{}, DefaultTreeLimits(), t.Context())
	for range treeCommandBufferCapacity {
		owner.inspections <- make(chan TreeInspection, 1)
	}
	select {
	case owner.inspections <- make(chan TreeInspection, 1):
		t.Fatal("inspection admission exceeded its bounded lane")
	default:
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := owner.inspect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission error=%v", err)
	}
	if _, err := (&Engine{}).InspectTree(ctx, ProcessID{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled public inspection error=%v", err)
	}
	if _, err := (&Engine{}).InspectTree(t.Context(), ProcessID{}); !errors.Is(err, ErrTreeNotFound) {
		t.Fatalf("unknown root inspection error=%v", err)
	}
	if _, err := (*Engine)(nil).InspectTree(t.Context(), ProcessID{}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("nil Engine inspection error=%v", err)
	}
}

func TestInspectionWaitAuthoritySurvivesRestoreWithoutChangingFacts(t *testing.T) {
	for _, scenario := range []struct {
		mode string
		kind WaitKind
	}{
		{mode: "external_wait", kind: WaitKindExternal},
		{mode: "wait:paused", kind: WaitKindChildren},
	} {
		t.Run(scenario.mode, func(t *testing.T) {
			deployment := newChildTestDeployment(t)
			engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := EncodePayload(childTestInput{Mode: scenario.mode})
			root, err := engine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, root, StatusWaiting)
			ctx, cancel := context.WithTimeout(t.Context(), treeRuntimeProgressTimeout)
			defer cancel()
			for {
				inspection := requireTreeInspection(t, engine, root.ID())
				report, found := inspection.Process(root.ID())
				kind, waiting := report.Snapshot.WaitKind()
				allIdle := true
				for _, process := range inspection.Processes {
					allIdle = allIdle && process.Work == ProcessWorkIdle
				}
				if found && waiting && kind == scenario.kind && allIdle {
					break
				}
				if contextErr := ctx.Err(); contextErr != nil {
					t.Fatalf("wait facts did not settle: inspection=%+v error=%v", inspection, contextErr)
				}
				runtime.Gosched()
			}
			before, err := engine.CaptureTree(t.Context(), root.ID())
			if err != nil {
				t.Fatal(err)
			}
			for range 32 {
				inspection := requireTreeInspection(t, engine, root.ID())
				report, found := inspection.Process(root.ID())
				kind, waiting := report.Snapshot.WaitKind()
				if !found || !waiting || kind != scenario.kind || !inspection.HeadDigest.Valid() || !inspection.IncarnationID.Valid() {
					t.Fatalf("acknowledged wait inspection=%+v kind=%s waiting=%t", inspection, kind, waiting)
				}
			}
			after, err := engine.CaptureTree(t.Context(), root.ID())
			if err != nil || after.Digest() != before.Digest() {
				t.Fatalf("pure inspection changed execution facts: %v", err)
			}
			restoredEngine, err := NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(before)})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := restoredEngine.RestoreTree(t.Context(), deployment, before)
			if err != nil {
				t.Fatal(err)
			}
			kind, waiting := inspectProcessSnapshot(t, restored).WaitKind()
			if !waiting || kind != scenario.kind {
				t.Fatalf("restored wait kind=%s waiting=%t", kind, waiting)
			}
			for _, instance := range []struct {
				engine *Engine
				root   *Process
			}{{engine, root}, {restoredEngine, restored}} {
				if killErr := instance.root.Kill(t.Context(), "inspection complete"); killErr != nil {
					t.Fatal(killErr)
				}
				mustAwait(t, instance.root)
				if kind, waiting := inspectProcessSnapshot(t, instance.root).WaitKind(); waiting || kind != "" {
					t.Fatalf("terminal Process retained a wait: %s, %t", kind, waiting)
				}
				if releaseErr := instance.engine.ReleaseTree(t.Context(), instance.root.ID()); releaseErr != nil {
					t.Fatal(releaseErr)
				}
				mustCloseEngine(t, instance.engine)
			}
		})
	}
}
