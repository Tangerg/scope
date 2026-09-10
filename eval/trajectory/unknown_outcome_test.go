package trajectory_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestRecorderPreservesHostFailureAsUnknownToolOutcome(t *testing.T) {
	recorder := &trajectory.Recorder{}
	process, engine := startRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{
		failure: interaction.HostFailure(errors.New("tool boundary unavailable")),
	}, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var unknown []agent.EffectID
	var owner *agent.Process
	for len(unknown) == 0 {
		inspection, captureErr := engine.InspectTree(ctx, process.ID())
		if captureErr != nil {
			t.Fatal(captureErr)
		}
		for _, report := range inspection.Processes {
			captured := report.Snapshot
			candidate, found := engine.Process(captured.ProcessID())
			if !found {
				t.Fatal("captured Process is missing")
			}
			ids := captured.UnknownEffectIDs()
			if len(ids) != 0 {
				unknown, owner = ids, candidate
				break
			}
		}
		rootReport, found := inspection.Process(process.ID())
		if !found || rootReport.Snapshot.Status().Terminal() {
			t.Fatalf("host failure lost the unresolved Tool Effect: %s", rootReport.Snapshot.Status())
		}
		if len(unknown) != 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if killErr := process.Kill(ctx, "retain unknown outcome for evaluation"); killErr != nil {
		t.Fatal(killErr)
	}
	result, err := process.Await(ctx)
	if err != nil || result.Status() != agent.StatusKilled {
		t.Fatalf("terminated Process status=%s error=%v", result.Status(), err)
	}
	if owner == nil || owner.ID() == process.ID() || len(result.Termination().UnresolvedEffectIDs()) != 0 {
		t.Fatal("unknown Effect must remain owned by its Tool child")
	}
	toolResult, err := owner.Await(ctx)
	if err != nil || toolResult.Status() != agent.StatusCanceled {
		t.Fatalf("Tool status=%s error=%v", toolResult.Status(), err)
	}
	unresolved := toolResult.Termination().UnresolvedEffectIDs()
	if len(unresolved) != 1 || len(unknown) != 1 || unresolved[0] != unknown[0] {
		t.Fatalf("terminal unresolved Effects=%v, want %v", unresolved, unknown)
	}
	recorded, err := recorder.Take(result)
	if err != nil {
		t.Fatal(err)
	}
	calls := recorded.ToolCalls()
	if len(calls) != 1 || calls[0].Outcome != trajectory.ToolOutcomeUnknown ||
		calls[0].Result != nil || !strings.Contains(calls[0].Failure, "tool boundary unavailable") {
		t.Fatalf("host failure recording = %#v", calls)
	}
}

type delayedToolObserver struct {
	*trajectory.Recorder
	invocation interaction.ToolInvocation
	settlement interaction.ToolSettlement
}

func (d *delayedToolObserver) OnToolSettled(_ context.Context, invocation interaction.ToolInvocation, settlement interaction.ToolSettlement) {
	d.invocation = invocation
	d.settlement = settlement
}

func TestIncompleteTakeRetainsObservationsUntilTheyCanBeValidated(t *testing.T) {
	recorder := &trajectory.Recorder{}
	observer := &delayedToolObserver{Recorder: recorder}
	result := runRecordedInteraction(t, recorder, observer, fixtureWeatherTool{})
	if _, err := recorder.Take(result); !errors.Is(err, trajectory.ErrIncompleteRecording) {
		t.Fatalf("Take without tool settlement error = %v", err)
	}
	recorder.OnToolSettled(t.Context(), observer.invocation, observer.settlement)
	recorded, err := recorder.Take(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded.ModelCalls()) != 2 || len(recorded.ToolCalls()) != 1 {
		t.Fatalf("failed Take consumed observations: %d model calls, %d tool calls", len(recorded.ModelCalls()), len(recorded.ToolCalls()))
	}
	if _, err := recorder.Take(result); err == nil {
		t.Fatal("successful Take did not release the recording")
	}
}
