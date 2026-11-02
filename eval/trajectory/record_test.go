package trajectory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestToolOutcomeDistinguishesPendingDispatchFailureAndToolResult(t *testing.T) {
	for _, test := range []struct {
		name string
		call trajectory.ToolCall
		want trajectory.ToolOutcome
	}{
		{"pending", trajectory.ToolCall{}, trajectory.ToolOutcomePending},
		{"dispatch failed", trajectory.ToolCall{Status: run.SettlementStatusFailed, Settlement: &interaction.ToolSettlement{Failure: "not executed"}}, trajectory.ToolOutcomeFailed},
		{"dispatch uncertain", trajectory.ToolCall{Status: run.SettlementStatusUnknown, Settlement: &interaction.ToolSettlement{Failure: "acknowledgment lost"}}, trajectory.ToolOutcomeUnknown},
		{"successful result", trajectory.ToolCall{Status: run.SettlementStatusSucceeded, Settlement: &interaction.ToolSettlement{Result: &chat.ToolResult{}}}, trajectory.ToolOutcomeSucceeded},
		{"tool error", trajectory.ToolCall{Status: run.SettlementStatusSucceeded, Settlement: &interaction.ToolSettlement{Result: &chat.ToolResult{IsError: true}}}, trajectory.ToolOutcomeError},
		{"input required", trajectory.ToolCall{Status: run.SettlementStatusSucceeded, Settlement: &interaction.ToolSettlement{InputRequired: true}}, trajectory.ToolOutcomeInputRequired},
		{"missing result", trajectory.ToolCall{Status: run.SettlementStatusSucceeded, Settlement: &interaction.ToolSettlement{}}, trajectory.ToolOutcomeInvalid},
		{"missing status", trajectory.ToolCall{Settlement: &interaction.ToolSettlement{}}, trajectory.ToolOutcomeInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.call.Outcome(); got != test.want {
				t.Fatalf("outcome=%q, want %q", got, test.want)
			}
			if test.want.Valid() != (test.want != trajectory.ToolOutcomeInvalid) {
				t.Fatalf("outcome validity contradicts classification %q", test.want)
			}
		})
	}
}

type approvalWeatherTool struct{}

func (approvalWeatherTool) Definition() tool.Definition { return fixtureWeatherTool{}.Definition() }
func (approvalWeatherTool) Call(_ context.Context, input tool.Invocation) (tool.Result, error) {
	if continuation, present := input.Continuation(); present {
		if string(continuation.Response()) != "true" {
			return tool.Result{}, errors.New("approval was not granted")
		}
		return tool.Result{Output: tool.NewTextOutput("approved weather")}, nil
	}
	schema, err := jsonschema.Parse([]byte(`{"type":"boolean"}`))
	if err != nil {
		return tool.Result{}, err
	}
	request, err := tool.NewInputRequest(tool.InputRequestConfig{Prompt: []byte(`"Allow the weather request?"`), ResponseSchema: schema, ContinuationState: []byte(`{"approved":false}`)})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{InputRequest: &request}, nil
}

func TestInputRequiredContinuationUsesTheOriginalEffectIdentity(t *testing.T) {
	handle, _, _ := startInteraction(t, &fixtureInteractionClient{}, []tool.Tool{approvalWeatherTool{}}, run.NewMemoryStore())
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var wait run.Wait
	for !wait.Valid() {
		view, err := handle.Inspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		root, ok := view.Process(handle.ID())
		if !ok {
			t.Fatal("root missing")
		}
		if len(root.Waits) > 0 {
			wait = root.Waits[0]
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("input request not published")
		case <-ticker.C:
		}
	}
	waiting := captureInteraction(t, handle)
	calls := waiting.ToolCalls()
	if len(calls) != 1 || calls[0].Outcome() != trajectory.ToolOutcomeInputRequired || calls[0].ContinuationOf.Valid() {
		t.Fatalf("input request=%+v", calls)
	}
	if waiting.HistoryComplete() {
		t.Fatal("pending input invented completion")
	}
	if err := handle.Answer(t.Context(), wait.Key, true); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	completed := captureInteraction(t, handle)
	calls = completed.ToolCalls()
	if len(calls) != 2 || calls[0].Outcome() != trajectory.ToolOutcomeInputRequired || calls[1].Outcome() != trajectory.ToolOutcomeSucceeded || calls[1].ContinuationOf != calls[0].EffectID {
		t.Fatalf("logical continuation identity changed: %+v", calls)
	}
	if calls[0].EffectID.Process != calls[1].EffectID.Process {
		t.Fatal("ordinary tool continuation spawned a second process")
	}
}

type uncertainWeatherTool struct{}

func (uncertainWeatherTool) Definition() tool.Definition { return fixtureWeatherTool{}.Definition() }
func (uncertainWeatherTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	failure, err := tool.NewCallError(tool.CallErrorConfig{Cause: errors.New("tool acknowledgment lost"), Evidence: tool.NewTextOutput("unconfirmed weather evidence")})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{}, failure
}

func TestUnknownToolRetainsEvidenceWithoutClaimingAFinalResult(t *testing.T) {
	handle, _, _ := startInteraction(t, &fixtureInteractionClient{}, []tool.Tool{uncertainWeatherTool{}}, run.NewMemoryStore())
	awaitUnknown(t, handle)
	actual := captureInteraction(t, handle)
	calls := actual.ToolCalls()
	if len(calls) != 1 || calls[0].Outcome() != trajectory.ToolOutcomeUnknown || calls[0].Settlement.Result != nil || calls[0].Settlement.Evidence == nil || calls[0].Settlement.Failure == "" {
		t.Fatalf("unknown evidence became a result: %+v", calls)
	}
	if _, err := actual.TreeUsage(); !errors.Is(err, trajectory.ErrIncompleteRecording) {
		t.Fatal("unresolved tree proved final usage", err)
	}
}

func TestToolSequenceUsesEffectCoordinatesInsteadOfProviderIDs(t *testing.T) {
	model := &fixtureInteractionClient{callID: "zzz", secondCallID: "aaa"}
	handle, _, _ := startInteraction(t, model, []tool.Tool{fixtureWeatherTool{}}, run.NewMemoryStore())
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	actual := captureInteraction(t, handle)
	paris, berlin := trajectory.ToolArguments(`{"city":"Paris"}`), trajectory.ToolArguments(`{"city":"Berlin"}`)
	expected := trajectory.Expectation{Status: run.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: "weather", Arguments: &paris}, {Name: "weather", Arguments: &berlin}}}}
	report, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: actual, Expected: expected})
	if err != nil || report.Verdict() != eval.VerdictPass {
		t.Fatalf("provider IDs reordered calls: %+v %v", report, err)
	}
	expected.Tools.Calls[0], expected.Tools.Calls[1] = expected.Tools.Calls[1], expected.Tools.Calls[0]
	report, err = (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: actual, Expected: expected})
	if err != nil || report.Verdict() != eval.VerdictFail {
		t.Fatalf("reversed semantic order passed: %+v %v", report, err)
	}
}
