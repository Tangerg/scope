package trajectory_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestRecorderPreservesHostFailureAsUnknownToolOutcome(t *testing.T) {
	recorder := &trajectory.Recorder{}
	result := runRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{
		failure: interaction.HostFailure(errors.New("tool boundary unavailable")),
	})
	if result.Status() != agent.StatusFailed {
		t.Fatalf("host failure Process status = %s", result.Status())
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
