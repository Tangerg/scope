package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestPendingSteerSurvivesExecutionStateRestoreWithExactSignalOrder(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	state := pendingSteerTestState(t)
	if err := state.validate(t.Context(), definition); err != nil {
		t.Fatal(err)
	}
	encoded, err := state.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(t.Context(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	restoredExecution, ok := restored.(*execution)
	if !ok {
		t.Fatalf("restored Execution type = %T", restored)
	}

	wantSignalIDs := state.PendingSteer.SignalIDs
	appliedSignalIDs, err := restoredExecution.state.applyPendingSteer()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(appliedSignalIDs, wantSignalIDs) {
		t.Fatalf("applied steer SignalIDs = %v, want %v", appliedSignalIDs, wantSignalIDs)
	}
	if restoredExecution.state.PendingSteer != nil {
		t.Fatal("applied pending steer remains in Execution state")
	}
	messages := restoredExecution.state.WorkingContext.Messages
	if len(messages) != 3 || messages[1].Text() != "first steer" || messages[2].Text() != "second steer" {
		t.Fatalf("restored WorkingContext messages = %#v", messages)
	}
	appliedSignalIDs[0] = run.SignalID{}
	if !state.PendingSteer.SignalIDs[0].Valid() {
		t.Fatal("restored steer attribution aliases source state")
	}
}

func TestExecutionStateRejectsIncompleteOrDuplicatePendingSteer(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	valid := pendingSteerTestState(t)
	firstID := valid.PendingSteer.SignalIDs[0]
	cases := map[string]*steerBatch{
		"missing Signal identity": {
			Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("missing identity"))},
		},
		"missing message": {SignalIDs: []run.SignalID{firstID}},
		"duplicate Signal identity": {
			Messages: []chat.Message{
				chat.NewUserMessage(chat.NewTextPart("first")),
				chat.NewUserMessage(chat.NewTextPart("second")),
			},
			SignalIDs: []run.SignalID{firstID, firstID},
		},
	}
	for name, pending := range cases {
		t.Run(name, func(t *testing.T) {
			state := pendingSteerTestState(t)
			state.PendingSteer = pending
			if err := state.validate(t.Context(), definition); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("Validate error = %v, want ErrInvalidExecutionState", err)
			}
		})
	}
}

func TestRoundCheckpointPropagatesInvalidSteer(t *testing.T) {
	for name, complete := range map[string]bool{"advancing": false, "complete": true} {
		t.Run(name, func(t *testing.T) {
			execution := childBatchTestExecution(t, childCallsTool, phaseAwaitingChildStarts)
			execution.state.ToolRound.ChildBatch = nil
			if complete {
				execution.state.ToolRound.Results = []toolCallResult{{Disposition: ResultSucceeded, Output: toolcontract.NewTextOutput("done")}}
			}
			if err := execution.state.validate(t.Context(), execution.definition); err != nil {
				t.Fatal(err)
			}
			id, err := run.ParseSignalID("signal:duplicate-steer")
			if err != nil {
				t.Fatal(err)
			}
			request, err := NewSteerSignal(id, chat.NewUserMessage(chat.NewTextPart("refine")))
			if err != nil {
				t.Fatal(err)
			}
			signal := signalFromRequest(t, request)
			transition, err := execution.Step(t.Context(), []run.Signal{signal, signal})
			if !errors.Is(err, ErrInvalidSteer) || !errors.Is(err, ErrInvalidExecutionState) || transition.Valid() {
				t.Fatalf("duplicate steer at %s: transition=%+v error=%v", name, transition, err)
			}
		})
	}
}

func TestDelegateWaitCollectorAcceptsSteerBeforeWaitOpened(t *testing.T) {
	steerID, err := run.ParseSignalID("signal:delegate-wait-steer")
	if err != nil {
		t.Fatal(err)
	}
	steerRequest, err := NewSteerSignal(
		steerID,
		chat.NewUserMessage(chat.NewTextPart("refine after Delegate settlement")),
	)
	if err != nil {
		t.Fatal(err)
	}
	steerSignal := signalFromRequest(t, steerRequest)
	waitOpenedSignal := childWaitOpenedTestSignal(t)

	opened, steer, consumed, err := collectChildWaitOpened([]run.Signal{
		steerSignal,
		waitOpenedSignal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opened.Valid() || consumed != 2 {
		t.Fatalf("opened valid = %t, consumed = %d", opened.Valid(), consumed)
	}
	if len(steer.SignalIDs) != 1 || steer.SignalIDs[0] != steerID ||
		len(steer.Messages) != 1 || steer.Messages[0].Text() != "refine after Delegate settlement" {
		t.Fatalf("collected steer = %#v", steer)
	}
}

func pendingSteerTestState(t testing.TB) executionState {
	t.Helper()
	call := chat.ToolCall{ID: "call_pending_steer", Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
	assistant := chat.NewAssistantMessage(chat.NewToolCallPart(call))
	response := &chat.Response{Output: &chat.Output{Message: &assistant, FinishReason: chat.FinishReasonToolCalls}}
	childID, err := run.ParseProcessID("process:pending-steer-child")
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := run.ParseWaitID("wait:pending-steer-child")
	if err != nil {
		t.Fatal(err)
	}
	firstSignalID, err := run.ParseSignalID("signal:pending-steer-first")
	if err != nil {
		t.Fatal(err)
	}
	secondSignalID, err := run.ParseSignalID("signal:pending-steer-second")
	if err != nil {
		t.Fatal(err)
	}
	return executionState{
		WorkingContext: &Input{Messages: []chat.Message{
			chat.NewUserMessage(chat.NewTextPart("initial")),
		}},
		ModelCallCount: 1,
		ToolRound: &toolCallRound{Response: response,
			ChildBatch: &childCallBatch{Kind: childCallsDelegate, WaitID: &waitID, Invocations: []*childInvocationState{{
				ProcessID: &childID,
			}}}},
		PendingSteer: &steerBatch{
			Messages: []chat.Message{
				chat.NewUserMessage(chat.NewTextPart("first steer")),
				chat.NewUserMessage(chat.NewTextPart("second steer")),
			},
			SignalIDs: []run.SignalID{firstSignalID, secondSignalID},
		},
	}
}

func signalFromRequest(t testing.TB, request run.SignalRequest) run.Signal {
	t.Helper()
	wire := struct {
		ID      run.SignalID    `json:"id"`
		Payload json.RawMessage `json:"payload"`
	}{
		ID: request.ID(), Payload: request.Payload(),
	}
	encoded, err := jsonv2.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var signal run.Signal
	if err := jsonv2.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}

func childWaitOpenedTestSignal(t testing.TB) run.Signal {
	t.Helper()
	payload := json.RawMessage(`{"operation":"child_wait_opened"}`)
	id, err := run.ParseSignalID("signal:engine:delegate-wait-opened")
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := run.ParseWaitID("wait:delegate-wait-opened")
	if err != nil {
		t.Fatal(err)
	}
	wire := struct {
		ID      run.SignalID    `json:"id"`
		WaitID  run.WaitID      `json:"wait_id"`
		Payload json.RawMessage `json:"payload"`
	}{
		ID: id, WaitID: waitID,
		Payload: payload,
	}
	encoded, err := jsonv2.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var signal run.Signal
	if err := jsonv2.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}
