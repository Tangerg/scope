package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

func TestScopedJoinRequiresDescendantCheckpointAcknowledgment(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "acknowledged"
		if reject {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dispatcher := newBlockingChildDispatcher("cleanup", "sibling")
				defer dispatcher.ReleaseAll()
				durability := &blockingCancellationCheckpointDurability{
					recordingTreeDurability: &recordingTreeDurability{},
					entered:                 make(chan struct{}), release: make(chan struct{}),
				}
				if reject {
					durability.err = errors.New("descendant checkpoint rejected")
				}
				release := sync.OnceFunc(func() { close(durability.release) })
				defer release()
				engine, err := NewEngine(EngineConfig{TreeDurability: durability})
				if err != nil {
					t.Fatal(err)
				}
				input, _ := EncodeInput("root")
				root, err := engine.Start(t.Context(), newScopeJoinDeployment(t, ChildWaitBoundaryDrained, dispatcher), input)
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					<-dispatcher.started
				}
				scope := directChildWithKey(t, engine, root, "scope")
				waitForStatus(t, scope, StatusPaused)
				if operationErr := scope.Resume(t.Context()); operationErr != nil {
					t.Fatal(operationErr)
				}
				_ = mustAwait(t, scope)
				joined := make(chan error, 1)
				go func() { joined <- scope.Join(t.Context()) }()
				dispatcher.Release("cleanup")
				<-durability.entered
				synctest.Wait()
				select {
				case joinErr := <-joined:
					t.Fatalf("join preceded checkpoint acknowledgment: %v", joinErr)
				default:
				}
				if inspectProcessSnapshot(t, root).Status() != StatusWaiting {
					t.Fatal("strategy observed subtree drain before acknowledgment")
				}
				release()
				joinErr := <-joined
				if reject {
					if !errors.Is(joinErr, durability.err) {
						t.Fatalf("rejected drain = %v", joinErr)
					}
					_ = awaitRuntimeError(t, root, durability.err)
				} else {
					if joinErr != nil {
						t.Fatal(joinErr)
					}
					waitForStatus(t, root, StatusPaused)
					if operationErr := root.Resume(t.Context()); operationErr != nil {
						t.Fatal(operationErr)
					}
					_ = mustAwait(t, root)
				}
				dispatcher.ReleaseAll()
				if operationErr := root.Join(t.Context()); !errors.Is(operationErr, durability.err) {
					t.Fatalf("root join = %v", operationErr)
				}
				mustCloseEngine(t, engine)
			})
		})
	}
}

func TestScopedJoinSeparatesResultsFromDescendantCleanup(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, boundary := range []ChildWaitBoundary{ChildWaitBoundaryResult, ChildWaitBoundaryDrained} {
			name := "ephemeral/" + boundary.String()
			if durable {
				name = "durable/" + boundary.String()
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					dispatcher := newBlockingChildDispatcher("cleanup", "sibling")
					defer dispatcher.ReleaseAll()
					config := EngineConfig{}
					if durable {
						config.TreeDurability = &recordingTreeDurability{}
					}
					engine, err := NewEngine(config)
					if err != nil {
						t.Fatal(err)
					}
					deployment := newScopeJoinDeployment(t, boundary, dispatcher)
					input, _ := EncodeInput("root")
					root, err := engine.Start(t.Context(), deployment, input)
					if err != nil {
						t.Fatal(err)
					}
					for range 2 {
						<-dispatcher.started
					}
					scope := directChildWithKey(t, engine, root, "scope")
					sibling := directChildWithKey(t, engine, root, "sibling")
					waitForStatus(t, scope, StatusPaused)
					if operationErr := scope.Resume(t.Context()); operationErr != nil {
						t.Fatal(operationErr)
					}
					if result := mustAwait(t, scope); result.Status() != StatusCompleted {
						t.Fatalf("scope result = %+v", result)
					}
					joined := make(chan error, 1)
					go func() { joined <- scope.Join(t.Context()) }()
					synctest.Wait()
					select {
					case joinErr := <-joined:
						t.Fatalf("scope joined before descendant cleanup: %v", joinErr)
					default:
					}
					wantBefore := StatusWaiting
					if boundary == ChildWaitBoundaryResult {
						wantBefore = StatusPaused
					}
					waitForStatus(t, root, wantBefore)
					canceled, cancel := context.WithCancel(t.Context())
					cancel()
					if operationErr := scope.Join(canceled); !errors.Is(operationErr, context.Canceled) {
						t.Errorf("canceled Join = %v", operationErr)
					}
					var interrupted TreeSnapshot
					if durable {
						checkpoints := config.TreeDurability.(*recordingTreeDurability).treeCheckpoints()
						interrupted = checkpoints[len(checkpoints)-1].TreeSnapshot()
					}
					dispatcher.Release("cleanup")
					if operationErr := <-joined; operationErr != nil {
						t.Fatal(operationErr)
					}
					waitForStatus(t, root, StatusPaused)
					if inspectProcessSnapshot(t, sibling).Status().Terminal() {
						t.Error("joining one scope terminated its sibling")
					}
					if operationErr := root.Resume(t.Context()); operationErr != nil {
						t.Fatal(operationErr)
					}
					if result := mustAwait(t, root); result.Status() != StatusCompleted {
						t.Fatalf("root result = %+v", result)
					}
					dispatcher.ReleaseAll()
					if operationErr := root.Join(t.Context()); operationErr != nil {
						t.Fatal(operationErr)
					}
					mustCloseEngine(t, engine)
					if !durable {
						return
					}
					recoveredEngine, err := NewEngine(EngineConfig{TreeDurability: &recordingTreeDurability{}})
					if err != nil {
						t.Fatal(err)
					}
					recoveredRoot, err := recoveredEngine.RestoreTree(t.Context(), deployment, interrupted)
					if err != nil {
						t.Fatal(err)
					}
					recoveredScope := directChildWithKey(t, recoveredEngine, recoveredRoot, "scope")
					if operationErr := recoveredScope.Join(t.Context()); operationErr != nil {
						t.Fatal(operationErr)
					}
					cleanup := directChildWithKey(t, recoveredEngine, recoveredScope, "cleanup")
					if result := mustAwait(t, cleanup); result.Status() != StatusCanceled || len(result.Termination().UnresolvedEffectIDs()) != 1 {
						t.Errorf("restored cleanup discarded remote uncertainty: %+v", result)
					}
					waitForStatus(t, recoveredRoot, StatusPaused)
					if operationErr := recoveredRoot.Kill(t.Context(), "finish the recovered fixture"); operationErr != nil {
						t.Fatal(operationErr)
					}
					if operationErr := recoveredRoot.Join(t.Context()); operationErr != nil {
						t.Fatal(operationErr)
					}
					if len(dispatcher.started) != 0 {
						t.Error("joined recovery replayed an uncertain operation")
					}
					mustCloseEngine(t, recoveredEngine)
				})
			})
		}
	}
}

func directChildWithKey(t *testing.T, engine *Engine, parent *Process, key string) *Process {
	t.Helper()
	for _, encoded := range directChildIDs(t, engine, parent.ID()) {
		id, _ := ParseProcessID(encoded)
		child, _ := engine.Process(id)
		if candidate, _ := child.Relation().ChildKey(); candidate.String() == key {
			return child
		}
	}
	t.Fatalf("child %q is missing", key)
	return nil
}

func TestJoinRetainsParentResultAndWaitsForFailedDescendantCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dispatcher := newBlockingChildDispatcher("cleanup", "sibling")
		defer dispatcher.ReleaseAll()
		failure := errors.New("sibling settlement storage failed")
		durability := &rejectingEffectDurability{
			recordingTreeDurability: &recordingTreeDurability{}, rejectedKind: EffectBoundarySettled, err: failure,
		}
		engine, err := NewEngine(EngineConfig{TreeDurability: durability})
		if err != nil {
			t.Fatal(err)
		}
		input, _ := EncodeInput("root")
		root, err := engine.Start(t.Context(), newScopeJoinDeployment(t, ChildWaitBoundaryDrained, dispatcher), input)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			<-dispatcher.started
		}
		scope := directChildWithKey(t, engine, root, "scope")
		cleanup := directChildWithKey(t, engine, scope, "cleanup")
		waitForStatus(t, scope, StatusPaused)
		if operationErr := scope.Resume(t.Context()); operationErr != nil {
			t.Fatal(operationErr)
		}
		result := mustAwait(t, scope)
		joined := make(chan error, 1)
		go func() { joined <- scope.Join(t.Context()) }()
		dispatcher.Release("sibling")
		cleanupFailure := awaitRuntimeError(t, cleanup, failure)
		synctest.Wait()
		select {
		case joinErr := <-joined:
			t.Fatalf("runtime fault abandoned descendant cleanup: %v", joinErr)
		default:
		}
		dispatcher.Release("cleanup")
		joinErr := <-joined
		runtimeErr, ok := errors.AsType[*RuntimeError](joinErr)
		if !ok || !errors.Is(joinErr, failure) || runtimeErr.ProcessID() != scope.ID() ||
			len(runtimeErr.UnresolvedEffectIDs()) != 1 || runtimeErr.UnresolvedEffectIDs()[0] != cleanupFailure.UnresolvedEffectIDs()[0] {
			t.Fatalf("scope join failure = %v", joinErr)
		}
		if after := mustAwait(t, scope); after.Status() != result.Status() || after.FinishedAt() != result.FinishedAt() {
			t.Error("descendant runtime failure changed the acknowledged parent result")
		}
		*runtimeErr = RuntimeError{}
		if retained := scope.Join(t.Context()); !errors.Is(retained, failure) {
			t.Error("caller mutation corrupted the retained join failure")
		}
		if operationErr := root.Join(t.Context()); !errors.Is(operationErr, failure) {
			t.Errorf("root join = %v", operationErr)
		}
		mustCloseEngine(t, engine)
	})
}

func TestDurableChildResultDoesNotWaitForUnrelatedDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dispatcher := newBlockingChildDispatcher("first", "second", "third")
		defer dispatcher.ReleaseAll()
		engine, err := NewEngine(EngineConfig{TreeDurability: &recordingTreeDurability{}})
		if err != nil {
			t.Fatal(err)
		}
		input, _ := EncodeInput(childTestInput{Mode: "wait:all"})
		root, err := engine.Start(t.Context(), newChildTestDeploymentWithDispatcher(t, dispatcher), input)
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			<-dispatcher.started
		}
		var first *Process
		for _, encoded := range directChildIDs(t, engine, root.ID()) {
			id, _ := ParseProcessID(encoded)
			child, _ := engine.Process(id)
			if key, _ := child.Relation().ChildKey(); key.String() == "first" {
				first = child
			}
		}
		if first == nil {
			t.Fatal("first child is missing")
		}
		if operationErr := first.Kill(t.Context(), "replace one worker"); operationErr != nil {
			t.Fatal(operationErr)
		}
		dispatcher.Release("first")
		resultReady := make(chan Result, 1)
		go func() {
			result, awaitErr := first.Await(context.Background())
			if awaitErr != nil {
				t.Error(awaitErr)
			}
			resultReady <- result
		}()
		synctest.Wait()
		select {
		case result := <-resultReady:
			if result.Status() != StatusKilled {
				t.Errorf("child status = %s", result.Status())
			}
		default:
			t.Error("child result acknowledgment waited for unrelated sibling Dispatch jobs")
		}
		dispatcher.ReleaseAll()
		_ = mustAwait(t, root)
		mustCloseEngine(t, engine)
	})
}
