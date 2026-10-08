package trajectory_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestRecorderPreservesHostFailureAsUnknownToolOutcome(t *testing.T) {
	recorder := &trajectory.Recorder{}
	callError, err := tool.NewCallError(tool.CallErrorConfig{Cause: errors.New("tool boundary unavailable"), Evidence: chat.NewTextToolOutput("partial observation")})
	if err != nil {
		t.Fatal(err)
	}
	process, engine := startRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{
		failure: interaction.HostFailure(callError),
	}, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var unknown []agent.EffectID
	var owner *agent.Process
	for len(unknown) == 0 {
		inspection, captureErr := engine.InspectTree(ctx, process.Relation().ProcessID())
		if captureErr != nil {
			t.Fatal(captureErr)
		}
		for _, report := range inspection.Processes {
			captured := report.Snapshot
			candidate, found := engine.Process(captured.Relation().ProcessID())
			if !found {
				t.Fatal("captured Process is missing")
			}
			ids := captured.UnknownEffectIDs()
			if len(ids) != 0 {
				unknown, owner = ids, candidate
				break
			}
		}
		rootReport, found := inspection.Process(process.Relation().ProcessID())
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
	if err != nil || result.Termination().Status() != agent.StatusKilled {
		t.Fatalf("terminated Process status=%s error=%v", result.Termination().Status(), err)
	}
	if owner == nil || owner.Relation().ProcessID() == process.Relation().ProcessID() || len(result.Termination().UnresolvedEffectIDs()) != 0 {
		t.Fatal("unknown Effect must remain owned by its Tool child")
	}
	toolResult, err := owner.Await(ctx)
	if err != nil || toolResult.Termination().Status() != agent.StatusCanceled {
		t.Fatalf("Tool status=%s error=%v", toolResult.Termination().Status(), err)
	}
	unresolved := toolResult.Termination().UnresolvedEffectIDs()
	if len(unresolved) != 1 || len(unknown) != 1 || unresolved[0] != unknown[0] {
		t.Fatalf("terminal unresolved Effects=%v, want %v", unresolved, unknown)
	}
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := recorded.ToolCalls()
	if len(calls) != 1 || calls[0].Outcome() != trajectory.ToolOutcomeUnknown ||
		calls[0].Result != nil || calls[0].Evidence == nil || calls[0].Evidence.Content[0].Text != "partial observation" || !strings.Contains(calls[0].Failure, "tool boundary unavailable") {
		t.Fatalf("host failure recording = %#v", calls)
	}
	calls[0].Evidence.Content[0].Text = "mutated"
	if recorded.ToolCalls()[0].Evidence.Content[0].Text != "partial observation" {
		t.Fatal("caller mutated retained evidence")
	}
	encoded, err := jsonv2.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	var decoded trajectory.Trajectory
	if decodeErr := jsonv2.Unmarshal(encoded, &decoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if decoded.ToolCalls()[0].Result != nil || decoded.ToolCalls()[0].Evidence.Content[0].Text != "partial observation" {
		t.Fatal("round trip promoted or lost non-final evidence")
	}
	config := trajectoryConfig(decoded)
	config.Coverage = interactionCoverage(config.Events)
	covered, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []trajectory.ToolOutcome{trajectory.ToolOutcomeSucceeded, trajectory.ToolOutcomeError} {
		if _, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: covered, Expected: trajectory.Expectation{
			Status: agent.StatusKilled, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: "weather", Outcome: outcome}}},
		}}); !errors.Is(err, trajectory.ErrIncompleteRecording) {
			t.Fatalf("unknown Tool result became a decided outcome: %v", err)
		}
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

func TestIncompleteTakeConsumesSessionAndLateCallbacksDoNotReopenIt(t *testing.T) {
	recorder := &trajectory.Recorder{}
	observer := &delayedToolObserver{Recorder: recorder}
	process := runRecordedInteraction(t, recorder, observer, fixtureWeatherTool{})
	incomplete, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	if incomplete.HistoryComplete() || incomplete.Gaps().UnpairedCalls != 1 || len(incomplete.ToolCalls()) != 1 || incomplete.ToolCalls()[0].Outcome() != trajectory.ToolOutcomeUnobserved {
		t.Fatalf("unpaired evidence was hidden: gaps=%+v calls=%+v", incomplete.Gaps(), incomplete.ToolCalls())
	}
	recorder.OnToolSettled(t.Context(), observer.invocation, observer.settlement)
	if _, err := recorder.Take(t.Context(), process, nil); !errors.Is(err, trajectory.ErrIncompleteRecording) {
		t.Fatalf("late callback reopened recording: %v", err)
	}
}

func TestMalformedToolSettlementBecomesObservationGap(t *testing.T) {
	recorder := &trajectory.Recorder{}
	observer := &delayedToolObserver{Recorder: recorder}
	process := runRecordedInteraction(t, recorder, observer, fixtureWeatherTool{})
	if observer.settlement.Result == nil {
		t.Fatal("fixture Tool did not settle with a result")
	}
	recorder.OnToolSettled(t.Context(), observer.invocation, interaction.ToolSettlement{
		Result: observer.settlement.Result, Evidence: new(chat.NewTextToolOutput("non-final output")),
	})
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatalf("malformed settlement discarded the recording: %v", err)
	}
	if gaps := recorded.Gaps(); gaps.DroppedCallObservations != 1 || gaps.UnpairedCalls != 1 {
		t.Fatalf("malformed settlement was not an explicit gap: %+v", gaps)
	}
	calls := recorded.ToolCalls()
	if len(calls) != 1 || calls[0].Outcome() != trajectory.ToolOutcomeUnobserved || calls[0].Result != nil || calls[0].Evidence != nil {
		t.Fatalf("malformed settlement became evidence: %+v", calls)
	}
}

type settlementOnlyObserver struct {
	*trajectory.Recorder
	invocation interaction.ToolInvocation
	settlement interaction.ToolSettlement
}

func (s *settlementOnlyObserver) OnToolStarted(context.Context, interaction.ToolInvocation) {}

func (s *settlementOnlyObserver) OnToolSettled(ctx context.Context, invocation interaction.ToolInvocation, settlement interaction.ToolSettlement) {
	s.invocation, s.settlement = invocation, settlement
	s.Recorder.OnToolSettled(ctx, invocation, settlement)
}

func TestLateToolStartPreservesObservedSettlement(t *testing.T) {
	recorder := new(trajectory.Recorder)
	observer := &settlementOnlyObserver{Recorder: recorder}
	process := runRecordedInteraction(t, recorder, observer, fixtureWeatherTool{})
	recorder.OnToolStarted(t.Context(), observer.invocation)
	recorder.OnToolSettled(t.Context(), observer.invocation, observer.settlement)
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gaps := recorded.Gaps(); gaps.DroppedCallObservations != 2 || gaps.UnpairedCalls != 1 {
		t.Fatalf("duplicate callbacks were not explicit observation gaps: %+v", gaps)
	}
	calls := recorded.ToolCalls()
	if len(calls) != 1 || calls[0].Outcome() != trajectory.ToolOutcomeSucceeded || calls[0].Result == nil {
		t.Fatalf("late start overwrote the settled tool call: %+v", calls)
	}
}
