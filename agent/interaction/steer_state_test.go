package interaction

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestPendingSteerSurvivesExecutionStateRestoreWithExactSignalOrder(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	state := pendingSteerTestState(t)
	if err := state.Validate(definition); err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(encoded)
	if err != nil {
		t.Fatal(err)
	}
	restoredExecution, ok := restored.(*execution)
	if !ok {
		t.Fatalf("restored Execution type = %T", restored)
	}

	wantSignalIDs := state.PendingSteer.SignalIDs
	appliedSignalIDs, err := restoredExecution.applyPendingSteer()
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
	appliedSignalIDs[0] = agent.SignalID{}
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
		"missing message": {SignalIDs: []agent.SignalID{firstID}},
		"duplicate Signal identity": {
			Messages: []chat.Message{
				chat.NewUserMessage(chat.NewTextPart("first")),
				chat.NewUserMessage(chat.NewTextPart("second")),
			},
			SignalIDs: []agent.SignalID{firstID, firstID},
		},
	}
	for name, pending := range cases {
		t.Run(name, func(t *testing.T) {
			state := pendingSteerTestState(t)
			state.PendingSteer = pending
			if err := state.Validate(definition); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("Validate error = %v, want ErrInvalidExecutionState", err)
			}
		})
	}
}

func TestDelegateWaitCollectorAcceptsSteerBeforeWaitOpened(t *testing.T) {
	steerID, err := agent.ParseSignalID("signal:delegate-wait-steer")
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

	opened, steer, consumed, err := collectChildWaitOpened([]agent.Signal{
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
	key, err := DelegateChildKey(1, call)
	if err != nil {
		t.Fatal(err)
	}
	childID, err := agent.ParseProcessID("process:pending-steer-child")
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := agent.ParseWaitID("wait:pending-steer-child")
	if err != nil {
		t.Fatal(err)
	}
	firstSignalID, err := agent.ParseSignalID("signal:pending-steer-first")
	if err != nil {
		t.Fatal(err)
	}
	secondSignalID, err := agent.ParseSignalID("signal:pending-steer-second")
	if err != nil {
		t.Fatal(err)
	}
	return executionState{
		Phase: phaseWaitingDelegates,
		WorkingContext: &chat.Request{Messages: []chat.Message{
			chat.NewUserMessage(chat.NewTextPart("initial")),
		}},
		ModelCallCount:         1,
		PendingModelResponse:   response,
		ActiveToolCallEndIndex: 1,
		DelegateSegment: &delegateSegmentState{Invocations: []delegateInvocationState{{
			ChildKey: &key, ChildProcessID: &childID,
		}}},
		WaitID: &waitID,
		PendingSteer: &steerBatch{
			Messages: []chat.Message{
				chat.NewUserMessage(chat.NewTextPart("first steer")),
				chat.NewUserMessage(chat.NewTextPart("second steer")),
			},
			SignalIDs: []agent.SignalID{firstSignalID, secondSignalID},
		},
	}
}

func signalFromRequest(t testing.TB, request agent.SignalRequest) agent.Signal {
	t.Helper()
	wire := struct {
		ID      agent.SignalID  `json:"id"`
		Payload json.RawMessage `json:"payload"`
	}{
		ID: request.ID(), Payload: request.Payload(),
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if err := json.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}

func childWaitOpenedTestSignal(t testing.TB) agent.Signal {
	t.Helper()
	payload := json.RawMessage(`{
		"operation":"child_wait_opened",
		"spec":{
			"key":"interaction.delegate.wait.test",
			"children":["process:delegate-wait-child"],
			"boundary":"subtree_drained",
			"condition":{"kind":"all"}
		}
	}`)
	id, err := agent.ParseSignalID("signal:delegate-wait-opened")
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := agent.ParseWaitID("wait:delegate-wait-opened")
	if err != nil {
		t.Fatal(err)
	}
	wire := struct {
		ID      agent.SignalID  `json:"id"`
		WaitID  agent.WaitID    `json:"wait_id"`
		Payload json.RawMessage `json:"payload"`
	}{
		ID: id, WaitID: waitID,
		Payload: payload,
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if err := json.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	return signal
}
