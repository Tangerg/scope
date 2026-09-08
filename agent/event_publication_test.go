package agent

import (
	"context"
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
		{name: "pause", mode: "leaf_pause", kind: TreeCheckpointParked, step: 1,
			facts: []string{EventStepCommitted, EventProcessPaused, EventProcessFinished}},
		{name: "completion", mode: "leaf", kind: TreeCheckpointTerminal, step: 1,
			facts: []string{EventStepCommitted, EventProcessFinished}},
		{name: "resumption", mode: "leaf_pause", kind: TreeCheckpointTerminal, step: 2,
			facts: []string{EventProcessResumed, EventStepCommitted, EventProcessFinished}},
	} {
		for _, fail := range []bool{false, true} {
			name := scenario.name + "/acknowledged"
			if fail {
				name = scenario.name + "/failed"
			}
			t.Run(name, func(t *testing.T) {
				durability := &inspectionDurability{
					recordingTreeDurability: &recordingTreeDurability{},
					checkpointKind:          scenario.kind,
					entered:                 make(chan inspectionCommit, 1), release: make(chan struct{}),
				}
				if fail {
					durability.failure = errors.New("checkpoint acknowledgment failed")
				}
				t.Cleanup(durability.unblock)
				listener := &recordingEventListener{}
				engine, err := NewEngine(EngineConfig{
					TreeDurability: durability, EventListeners: []EventListener{listener},
				})
				if err != nil {
					t.Fatal(err)
				}
				input, _ := EncodeInput(childTestInput{Mode: scenario.mode})
				root, err := engine.Start(t.Context(), newChildTestDeployment(t), input)
				if err != nil {
					t.Fatal(err)
				}
				if scenario.step == 2 {
					waitForStatus(t, root, StatusPaused)
					if resumeErr := root.Resume(t.Context()); resumeErr != nil {
						t.Fatal(resumeErr)
					}
				}
				receiveTreeRuntimeProbe(t, durability.entered)
				unconfirmed := func(event Event) bool {
					step, _ := event.StepSequence()
					return event.Name() == EventStepCommitted && step == scenario.step ||
						event.Name() == EventProcessFinished ||
						scenario.step == 1 && event.Name() == EventProcessPaused ||
						scenario.step == 2 && event.Name() == EventProcessResumed
				}
				for _, event := range listener.snapshot() {
					if unconfirmed(event) {
						t.Errorf("published %s before acknowledgment", event.Name())
					}
				}
				durability.unblock()
				if !fail && scenario.kind == TreeCheckpointParked {
					waitForStatus(t, root, StatusPaused)
					if resumeErr := root.Resume(t.Context()); resumeErr != nil {
						t.Fatal(resumeErr)
					}
				}
				_, err = root.Await(t.Context())
				if !errors.Is(err, durability.failure) {
					t.Fatalf("Await error=%v, want %v", err, durability.failure)
				}
				if releaseErr := engine.ReleaseTree(t.Context(), root.ID()); releaseErr != nil {
					t.Fatal(releaseErr)
				}
				if err := engine.Close(); err != nil {
					t.Fatal(err)
				}
				wantCheckpoints := []TreeCheckpointKind{TreeCheckpointStart}
				if scenario.mode == "leaf_pause" {
					wantCheckpoints = append(wantCheckpoints, TreeCheckpointParked)
				}
				if !fail || scenario.kind == TreeCheckpointTerminal {
					wantCheckpoints = append(wantCheckpoints, TreeCheckpointTerminal)
				}
				var checkpointKinds []TreeCheckpointKind
				for _, checkpoint := range durability.treeCheckpoints() {
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
	mu   sync.Mutex
	head TreeSnapshot
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
	return e.install(checkpoint.TreeSnapshot())
}

func (e *eventPublicationDurability) CommitEffect(_ context.Context, boundary EffectBoundary) error {
	return e.install(boundary.TreeSnapshot())
}

func TestChildEventsDescribeAcknowledgedTreeState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		durability := &eventPublicationDurability{}
		sequences := make(map[ProcessID]uint64)
		var acceptedSignals int
		listener := EventListenerFunc(func(_ context.Context, event Event) {
			sequences[event.ProcessID()]++
			if event.ProcessSequence() != sequences[event.ProcessID()] {
				t.Errorf("%s publication sequence=%d, want %d", event.Name(), event.ProcessSequence(), sequences[event.ProcessID()])
			}
			if event.Phase() != EventPhaseCommitted {
				return
			}
			durability.mu.Lock()
			head := durability.head
			durability.mu.Unlock()
			snapshot := snapshotByID(head.ProcessSnapshots(), event.ProcessID())
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
		engine, err := NewEngine(EngineConfig{TreeDurability: durability, EventListeners: []EventListener{listener}})
		if err != nil {
			t.Fatal(err)
		}
		dispatcher := newBlockingChildDispatcher("first", "second", "third")
		t.Cleanup(dispatcher.ReleaseAll)
		input, _ := EncodeInput(childTestInput{Mode: "wait:all"})
		root, err := engine.Start(t.Context(), newChildTestDeploymentWithDispatcher(t, dispatcher), input)
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			<-dispatcher.started
		}
		synctest.Wait()
		dispatcher.ReleaseAll()
		result := awaitResult(t, root)
		if result.Status() != StatusCompleted {
			t.Errorf("root status=%s", result.Status())
		}
		if releaseErr := engine.ReleaseTree(t.Context(), root.ID()); releaseErr != nil {
			t.Fatal(releaseErr)
		}
		if err := engine.Close(); err != nil {
			t.Fatal(err)
		}
		if acceptedSignals == 0 {
			t.Fatal("child completion did not publish a Signal fact")
		}
	})
}

func TestRestoredProcessStartsANewPublicationSequence(t *testing.T) {
	listener := &recordingEventListener{}
	engine, _ := NewEngine(EngineConfig{EventListeners: []EventListener{listener}})
	deployment := newChildTestDeployment(t)
	input, _ := EncodeInput(childTestInput{Mode: "leaf_pause"})
	root, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, root, StatusPaused)
	snapshot, err := engine.CaptureTree(t.Context(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if killErr := root.Kill(t.Context(), "release original activation"); killErr != nil {
		t.Fatal(killErr)
	}
	if releaseErr := engine.ReleaseTree(t.Context(), root.ID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	previousEvents := len(listener.snapshot())
	root, err = engine.RestoreTree(t.Context(), deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if resumeErr := root.Resume(t.Context()); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	_ = awaitResult(t, root)
	if releaseErr := engine.ReleaseTree(t.Context(), root.ID()); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if err := engine.Close(); err != nil {
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
		durability := &recordingTreeDurability{}
		listener := &recordingEventListener{}
		engine, _ := NewEngine(EngineConfig{TreeDurability: durability, EventListeners: []EventListener{listener}})
		deployment, probe := newTreeRuntimeTestDeployment(t)
		input, _ := EncodeInput(treeRuntimeTestInput{Role: treeRuntimeRoleBlocked})
		root, err := engine.Start(t.Context(), deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		<-probe.blockedStepStarted
		if err := root.Pause(t.Context(), "same pause"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		checkpoints := durability.treeCheckpoints()
		if len(checkpoints) != 2 || checkpoints[1].Kind() != TreeCheckpointParked {
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
		if len(durability.treeCheckpoints()) != len(checkpoints) {
			t.Error("publication alone caused another commit of the same state")
		}
		var pauses, resumptions int
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
		if releaseErr := engine.ReleaseTree(t.Context(), root.ID()); releaseErr != nil {
			t.Fatal(releaseErr)
		}
		if err := engine.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
