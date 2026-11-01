package run

import (
	"context"
	startrand "crypto/rand"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
)

func TestCommittedEventsWaitForDurabilityAcknowledgment(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		mode  string
		kind  TreeCheckpointKind
		step  uint64
		facts []string
	}{
		{name: "pause", mode: "leaf_pause", kind: TreeCheckpointKindParked, step: 1,
			facts: []string{EventStepCommitted, EventProcessPaused, EventProcessFinished}},
		{name: "completion", mode: "leaf", kind: TreeCheckpointKindTerminal, step: 1,
			facts: []string{EventStepCommitted, EventProcessFinished}},
		{name: "resumption", mode: "leaf_pause", kind: TreeCheckpointKindTerminal, step: 2,
			facts: []string{EventProcessResumed, EventStepCommitted, EventProcessFinished}},
	} {
		for _, fail := range []bool{false, true} {
			name := scenario.name + "/acknowledged"
			if fail {
				name = scenario.name + "/failed"
			}
			t.Run(name, func(t *testing.T) {
				committer := &inspectionDurability{
					recordingTreeCommitter: &recordingTreeCommitter{},
					checkpointKind:         scenario.kind,
					entered:                make(chan inspectionCommit, 1), release: make(chan struct{}),
				}
				if fail {
					committer.failure = errors.New("checkpoint acknowledgment failed")
				}
				t.Cleanup(committer.unblock)
				listener := &recordingEventListener{}
				engine, err := New(t.Context(), Config{
					Store: committer, EventListeners: []EventListener{listener},
				})
				if err != nil {
					t.Fatal(err)
				}
				input, _ := EncodePayload(childTestInput{Mode: scenario.mode})
				root, err := engine.Start(t.Context(), newChildTestDeployment(t), StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
				if err != nil {
					t.Fatal(err)
				}
				if scenario.step == 2 {
					waitForStatus(t, root, StatusPaused)
					if resumeErr := root.Resume(t.Context()); resumeErr != nil {
						t.Fatal(resumeErr)
					}
				}
				receiveTreeRuntimeProbe(t, committer.entered)
				unconfirmed := func(event Event) bool {
					step, _ := event.StepSequence()
					return event.Name() == EventStepCommitted && step == scenario.step ||
						event.Name() == EventProcessFinished ||
						scenario.step == 1 && event.Name() == EventProcessPaused ||
						scenario.step == 2 && event.Name() == EventProcessResumed
				}
				if flushObservationsErr := engine.FlushObservations(t.Context()); flushObservationsErr != nil {
					t.Fatal(flushObservationsErr)
				}
				for _, event := range listener.snapshot() {
					if unconfirmed(event) {
						t.Errorf("published %s before acknowledgment", event.Name())
					}
				}
				committer.unblock()
				if !fail && scenario.kind == TreeCheckpointKindParked {
					waitForStatus(t, root, StatusPaused)
					if resumeErr := root.Resume(t.Context()); resumeErr != nil {
						t.Fatal(resumeErr)
					}
				}
				_, err = root.Await(t.Context())
				if !errors.Is(err, committer.failure) {
					t.Fatalf("Await error=%v, want %v", err, committer.failure)
				}
				if releaseErr := engine.ReleaseTree(t.Context(), root.Relation().ProcessID()); releaseErr != nil {
					t.Fatal(releaseErr)
				}
				if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Fatal(err)
				}
				wantCheckpoints := []TreeCheckpointKind{TreeCheckpointKindStart}
				if scenario.mode == "leaf_pause" {
					wantCheckpoints = append(wantCheckpoints, TreeCheckpointKindParked)
				}
				if !fail || scenario.kind == TreeCheckpointKindTerminal {
					wantCheckpoints = append(wantCheckpoints, TreeCheckpointKindTerminal)
				}
				var checkpointKinds []TreeCheckpointKind
				for _, checkpoint := range committer.treeCheckpoints() {
					checkpointKinds = append(checkpointKinds, checkpoint.Kind())
				}
				if !slices.Equal(checkpointKinds, wantCheckpoints) {
					t.Errorf("checkpoints=%v, want %v", checkpointKinds, wantCheckpoints)
				}
				var published []string
				for index, event := range listener.snapshot() {
					if event.ProcessSequence() != uint64(index+1) {
						t.Errorf("event[%d] sequence=%d", index, event.ProcessSequence())
					}
					if unconfirmed(event) {
						published = append(published, event.Name())
					}
				}
				wantFacts := scenario.facts
				if fail {
					wantFacts = nil
				}
				if !slices.Equal(published, wantFacts) {
					t.Errorf("published facts=%v, want %v", published, wantFacts)
				}
			})
		}
	}
}

type eventPublicationDurability struct {
	start StartReceipt
	mu    sync.Mutex
	head  TreeSnapshot
}

func (e *eventPublicationDurability) install(snapshot TreeSnapshot) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.head = snapshot
	return nil
}

func (e *eventPublicationDurability) ActivateTree(_ context.Context, activation TreeActivation) error {
	return e.install(activation.TreeSnapshot())
}

func (e *eventPublicationDurability) CommitCheckpoint(_ context.Context, checkpoint TreeCheckpoint) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rec, ok := checkpoint.Start(); ok {
		e.start = rec
	}
	e.head = checkpoint.TreeSnapshot()
	return nil
}

func (e *eventPublicationDurability) CommitEffect(_ context.Context, boundary EffectBoundary) error {
	return e.install(boundary.TreeSnapshot())
}

func TestChildEventsDescribeAcknowledgedTreeState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		committer := &eventPublicationDurability{}
		sequences := make(map[ProcessID]uint64)
		var acceptedSignals int
		listener := EventListenerFunc(func(_ context.Context, event Event) {
			sequences[event.Relation().ProcessID()]++
			if event.ProcessSequence() != sequences[event.Relation().ProcessID()] {
				t.Errorf("%s publication sequence=%d, want %d", event.Name(), event.ProcessSequence(), sequences[event.Relation().ProcessID()])
			}
			if event.Phase() != EventPhaseCommitted {
				return
			}
			committer.mu.Lock()
			head := committer.head
			committer.mu.Unlock()
			snapshot := head.state.processSnapshot(event.Relation().ProcessID())
			if !snapshot.Valid() {
				t.Errorf("%s published before Process admission", event.Name())
				return
			}
			if step, present := event.StepSequence(); present && step > snapshot.Usage().CommittedSteps {
				t.Errorf("Step %d published beyond acknowledged Step %d", step, snapshot.Usage().CommittedSteps)
			}
			if fact, accepted := event.SignalAccepted(); accepted {
				acceptedSignals++
				wire, err := snapshot.wire()
				if err != nil {
					t.Error(err)
					return
				}
				if !slices.ContainsFunc(wire.Mailbox.Signals, func(record signalRecordWire) bool { return record.ID == fact.SignalID() }) {
					t.Errorf("Signal %s published before durable acceptance", fact.SignalID())
				}
			}
		})
		engine, err := New(t.Context(), Config{Store: committer, EventListeners: []EventListener{listener}})
		if err != nil {
			t.Fatal(err)
		}
		dispatcher := newBlockingChildDispatcher("first", "second", "third")
		t.Cleanup(dispatcher.ReleaseAll)
		input, _ := EncodePayload(childTestInput{Mode: "wait:all"})
		root, err := engine.Start(t.Context(), newChildTestDeploymentWithDispatcher(t, dispatcher), StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			<-dispatcher.started
		}
		synctest.Wait()
		dispatcher.ReleaseAll()
		result := awaitResult(t, root)
		if result.Termination().Status() != StatusCompleted {
			t.Errorf("root status=%s", result.Termination().Status())
		}
		if releaseErr := engine.ReleaseTree(t.Context(), root.Relation().ProcessID()); releaseErr != nil {
			t.Fatal(releaseErr)
		}
		if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Fatal(err)
		}
		if acceptedSignals == 0 {
			t.Fatal("child completion did not publish a Signal fact")
		}
	})
}

func TestRestoredProcessStartsANewPublicationSequence(t *testing.T) {
	listener := &recordingEventListener{}
	engine, _ := New(t.Context(), Config{Store: NewMemoryStore(), EventListeners: []EventListener{listener}})
	deployment := newChildTestDeployment(t)
	input, _ := EncodePayload(childTestInput{Mode: "leaf_pause"})
	root, err := engine.Start(t.Context(), deployment, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, root, StatusPaused)
	snapshot, err := engine.CaptureTree(t.Context(), root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if killErr := root.Kill(t.Context(), "release original activation"); killErr != nil {
		t.Fatal(killErr)
	}
	if releaseErr := engine.ReleaseTree(t.Context(), root.Relation().ProcessID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	mustCloseEngine(t, engine)
	previousEvents := len(listener.snapshot())
	engine = controlValue(New(t.Context(), Config{Store: newSnapshotTestCommitter(snapshot), EventListeners: []EventListener{listener}}))
	root, err = engine.Recover(t.Context(), deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if resumeErr := root.Resume(t.Context()); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	_ = awaitResult(t, root)
	if releaseErr := engine.ReleaseTree(t.Context(), root.Relation().ProcessID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	events := listener.snapshot()[previousEvents:]
	if len(events) == 0 || events[0].Name() != EventProcessRestored {
		t.Fatal("restored activation did not identify its first fact")
	}
	for index, event := range events {
		if event.ProcessSequence() != uint64(index+1) {
			t.Errorf("restored event[%d] sequence=%d", index, event.ProcessSequence())
		}
	}
}

func TestEquivalentPausedStatePublishesWithoutAnotherCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		committer := &recordingTreeCommitter{}
		listener := &recordingEventListener{}
		engine, _ := New(t.Context(), Config{Store: committer, EventListeners: []EventListener{listener}})
		deployment, probe := newTreeRuntimeTestDeployment(t)
		input, _ := EncodePayload(treeRuntimeTestInput{Role: treeRuntimeRoleBlocked})
		root, err := engine.Start(t.Context(), deployment, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
		if err != nil {
			t.Fatal(err)
		}
		<-probe.blockedStepStarted
		if err := root.Pause(t.Context(), "same pause"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		checkpoints := committer.treeCheckpoints()
		if len(checkpoints) != 2 || checkpoints[1].Kind() != TreeCheckpointKindParked {
			t.Fatal("initial pause was not acknowledged")
		}
		if resumeErr := root.Resume(t.Context()); resumeErr != nil {
			t.Fatal(resumeErr)
		}
		synctest.Wait()
		if err := root.Pause(t.Context(), "same pause"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(committer.treeCheckpoints()) != len(checkpoints) {
			t.Error("publication alone caused another commit of the same state")
		}
		var pauses, resumptions int
		if err := engine.FlushObservations(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, event := range listener.snapshot() {
			switch event.Name() {
			case EventProcessPaused:
				pauses++
			case EventProcessResumed:
				resumptions++
			}
		}
		if pauses != 2 || resumptions != 1 {
			t.Errorf("acknowledged state left events pending: pauses=%d, resumptions=%d", pauses, resumptions)
		}
		if killErr := root.Kill(t.Context(), "test complete"); killErr != nil {
			t.Fatal(killErr)
		}
		if releaseErr := engine.ReleaseTree(t.Context(), root.Relation().ProcessID()); releaseErr != nil {
			t.Fatal(releaseErr)
		}
		if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Fatal(err)
		}
	})
}

func (e *eventPublicationDurability) LookupStart(_ context.Context, id StartID) (StartReceipt, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.start, e.start.Valid() && e.start.ID == id, nil
}
