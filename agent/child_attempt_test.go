package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestChildStartCapacityRejectionFinishesFailedAttempt(t *testing.T) {
	owner, parent := newChildCompletionTestProcess(t)
	owner.writer.committer = &recordingTreeCommitter{}
	if err := owner.engine.reserveProcessStart(parent.handle.relation); err != nil {
		t.Fatal(err)
	}
	owner.engine.publishProcessStart(parent.handle)
	t.Cleanup(func() { delete(owner.engine.processes, parent.handle.processID()) })
	var events []Event
	owner.events.observation = newObservationBus([]EventListener{EventListenerFunc(func(_ context.Context, event Event) {
		events = append(events, event)
	})}, nil, 0)
	effectID := parent.handle.processID().effectID(1, 0)
	spec := childTestSpec(controlValue(ParseChildKey("large")), parent.deployment.DeploymentRef(),
		controlValue(EncodePayload(childTestInput{Mode: "leaf_effect:" + strings.Repeat("x", 32<<10)})))
	parent.prepared = &preparedStep{
		CandidateState: parent.committedExecutionState, Intent: controlValue(Continue(0)),
		Effects: preparedEffects{{ID: effectID, Effect: controlValue(NewChildStartEffect(spec)), progress: &effectProgress{}}},
	}
	base := controlValue(owner.captureTree())
	owner.treeLimits.MaxSnapshotBytes = NewQuota(uint64(len(base.JSON())) + 4096)
	owner.writer.acknowledged = controlValue(owner.captureTree())
	owner.startPreparedEffect(parent, 0, &parent.prepared.Effects[0])
	completion := receiveTreeRuntimeProbe(t, owner.completions)
	if !completion.result.(childStartJobResult).started() {
		t.Fatal("child initialization did not succeed before owner capacity admission")
	}
	owner.applyCompletion(completion)
	owner.applyTreeCommitCompletion(receiveTreeRuntimeProbe(t, owner.writer.done))
	assertFrameworkAttemptsClosed(t, events)
	for _, event := range events {
		if fact, finished := event.EffectFinished(); finished && fact.SettlementStatus() != SettlementStatusFailed {
			t.Fatalf("capacity-rejected child attempt status=%s", fact.SettlementStatus())
		}
	}
	if owner.members.len() != 1 || parent.prepared.Effects[0].settlement().Status() != SettlementStatusFailed {
		t.Fatal("capacity rejection published a child or lost its failed settlement")
	}
}

func TestChildStartAttemptFinishesBeforePublication(t *testing.T) {
	for _, rejectCommit := range []bool{false, true} {
		t.Run(fmt.Sprintf("reject_commit=%t", rejectCommit), func(t *testing.T) {
			committer := &inspectionDurability{
				recordingTreeCommitter: &recordingTreeCommitter{}, checkpointKind: TreeCheckpointKindChildStart,
				entered: make(chan inspectionCommit, 1), release: make(chan struct{}),
			}
			if rejectCommit {
				committer.failure = errors.New("child publication rejected")
			}
			t.Cleanup(committer.unblock)
			events := make(chan Event, 64)
			engine := controlValue(NewEngine(EngineConfig{
				TreeCommitter:  committer,
				EventListeners: []EventListener{EventListenerFunc(func(_ context.Context, event Event) { events <- event })},
			}))
			t.Cleanup(func() {
				committer.unblock()
				closeEngineEventually(t, engine)
			})
			root := controlValue(engine.Start(t.Context(), newChildTestDeployment(t), controlValue(EncodePayload(childTestInput{Mode: "parent"}))))
			commit := receiveTreeRuntimeProbe(t, committer.entered)
			var observed []Event
			for {
				event := receiveTreeRuntimeProbe(t, events)
				observed = append(observed, event)
				if fact, finished := event.EffectFinished(); finished && fact.Target() == EffectTargetFramework {
					if fact.SettlementStatus() != SettlementStatusSucceeded {
						t.Fatalf("initialized child attempt status=%s", fact.SettlementStatus())
					}
					break
				}
			}
			for _, snapshot := range commit.next.ProcessSnapshots() {
				if !snapshot.Relation().IsRoot() {
					if _, published := engine.Process(snapshot.Relation().ProcessID()); published {
						t.Fatal("attempt completion published an unacknowledged child")
					}
				}
			}
			committer.unblock()
			owner := root.handle.treeRuntime()
			<-owner.done
			if _, err := root.Await(t.Context()); !errors.Is(err, committer.failure) {
				t.Fatalf("root outcome error=%v, want %v", err, committer.failure)
			}
			for len(events) != 0 {
				observed = append(observed, <-events)
			}
			assertFrameworkAttemptsClosed(t, observed)
			if rejectCommit && owner.members.len() != 1 {
				t.Fatal("failed publication retained a prospective child")
			}
			mustCloseEngine(t, engine)
		})
	}
}

func assertFrameworkAttemptsClosed(t *testing.T, events []Event) {
	t.Helper()
	starts, finishes := make(map[EffectAttemptID]int), make(map[EffectAttemptID]int)
	for _, event := range events {
		if fact, started := event.EffectStarted(); started && fact.Target() == EffectTargetFramework {
			starts[fact.AttemptID()]++
		}
		if fact, finished := event.EffectFinished(); finished && fact.Target() == EffectTargetFramework {
			finishes[fact.AttemptID()]++
		}
	}
	if len(starts) == 0 || len(starts) != len(finishes) {
		t.Fatalf("Framework attempt identities started=%d finished=%d", len(starts), len(finishes))
	}
	for attempt, count := range starts {
		if count != 1 || finishes[attempt] != 1 {
			t.Fatalf("attempt %s started=%d finished=%d", attempt, count, finishes[attempt])
		}
	}
}
