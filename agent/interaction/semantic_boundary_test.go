package interaction

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestOutputRequiresOneCompleteSuccessfulSemanticVariant(t *testing.T) {
	result := chat.ToolResult{ID: "call", Name: "echo", Output: tool.NewTextOutput("done")}
	for _, output := range []Output{
		{},
		{ModelCalls: 1},
		{ModelCalls: 1, ModelResponse: finalResponse("done"), DirectToolResults: []chat.ToolResult{result}},
		{ModelCalls: 1, ModelResponse: &chat.Response{}},
		{ModelCalls: 1, ModelResponse: &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}}},
		{ModelCalls: 1, ModelResponse: callResponse(requested("echo", "call"))},
		{ModelCalls: 1, DirectToolResults: []chat.ToolResult{{Name: "echo", Output: result.Output}}},
		{ModelCalls: 1, DirectToolResults: []chat.ToolResult{{ID: "call", Name: "echo", Output: result.Output, IsError: true}}},
		{ModelCalls: 1, DirectToolResults: []chat.ToolResult{result, result}},
	} {
		if err := output.Validate(); !errors.Is(err, ErrInvalidResult) {
			t.Fatal("unfinished, ambiguous, or failed evidence became a final output", err)
		}
	}
	for _, output := range []Output{{ModelCalls: 1, ModelResponse: finalResponse("done")}, {ModelCalls: 1, DirectToolResults: []chat.ToolResult{result}}} {
		if err := output.Validate(); err != nil {
			t.Fatal("complete evidence was rejected", err)
		}
	}
}

func TestDefinitionStartRejectsNonProtocolInput(t *testing.T) {
	definition := mustValue(newDefinition(Config{Name: "test.start.contract", Description: "Reject malformed interaction input."}))
	for _, raw := range []string{`null`, `[]`, `42`, `{}`, `{"messages":[]}`, `{"messages":[{"role":"user","parts":[{"kind":"text","text":"work"}]}],"unknown":true}`} {
		if execution, err := definition.Start(mustValue(run.ParsePayload([]byte(raw)))); !errors.Is(err, ErrInvalidInput) || execution != nil {
			t.Fatal("input without the current interaction contract was admitted", raw, err)
		}
	}
}

func TestExternalGuidanceAndDispatcherOutcomesHaveDisjointAdmission(t *testing.T) {
	guidance := steer(t, "guidance")
	outcome := mustValue(run.NewSettlement(run.SettlementStatusSucceeded, guidance.Payload.JSON()))
	if _, err := ModelSettlementFromRecord(run.EffectSettled{Outcome: outcome}); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("external guidance became a model outcome", err)
	}
	if _, err := ToolSettlementFromRecord(requested("echo", "call"), run.EffectSettled{Outcome: outcome}); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("external guidance became a tool outcome", err)
	}
	definition := mustValue(newDefinition(Config{Name: "test.guidance", Description: "Admit only unaddressed external guidance."}))
	for _, signal := range []run.Signal{
		{ID: guidance.ID, Payload: mustValue(run.ParsePayload([]byte(`{"steer":{"messages":[]}}`)))},
		{ID: guidance.ID, Wait: mustValue(run.ParseWaitKey("question")), Payload: guidance.Payload},
	} {
		execution := mustValue(definition.Start(mustValue(run.EncodePayload(testInput()))))
		advance(t, execution, 1)
		turn := testTurn(2)
		turn.Messages = []run.Message{signal}
		if transition, err := execution.Step(t.Context(), turn); !errors.Is(err, ErrInvalidSteer) || transition.Valid() {
			t.Fatal("invalid external guidance changed the pending model operation", err)
		}
	}
}

func pendingToolExecution(t *testing.T, definition *definition) (run.Execution, run.EffectID) {
	t.Helper()
	execution := mustValue(definition.Start(mustValue(run.EncodePayload(testInput()))))
	advance(t, execution, 1)
	advance(t, execution, 2, run.Completion{Effect: testTurn(1).EffectID(0), Outcome: mustValue((modelCallResult{Response: callResponse(requested("echo", "call"))}).settlement())})
	transition := advance(t, execution, 3)
	if len(transition.Effects()) != 1 {
		t.Fatal("fixture did not establish a direct tool obligation")
	}
	return execution, testTurn(3).EffectID(0)
}

func TestToolMessagesCannotChangeAnUnrelatedObligation(t *testing.T) {
	tools := toolSetTest(t, scriptTool{name: "echo"})
	definition := mustValue(newDefinition(Config{Name: "test.tool.messages", Description: "Accept only the exact tool obligation.", Tools: tools}))
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`null`)))
	questionOutcome := mustValue((toolDispatchResult{InputRequest: &question}).settlement())
	wrongOutcome := mustValue((modelCallResult{Response: finalResponse("another operation")}).settlement())
	for _, row := range []struct {
		name     string
		question bool
		message  func(run.EffectID, run.WaitKey) run.Message
		failure  error
	}{
		{"foreign completion", false, func(_ run.EffectID, _ run.WaitKey) run.Message {
			return run.Completion{Effect: testTurn(100).EffectID(0), Outcome: questionOutcome}
		}, ErrInvalidProtocol},
		{"wrong operation", false, func(effect run.EffectID, _ run.WaitKey) run.Message {
			return run.Completion{Effect: effect, Outcome: wrongOutcome}
		}, ErrInvalidProtocol},
		{"malformed completion", false, func(effect run.EffectID, _ run.WaitKey) run.Message {
			return run.Completion{Effect: effect, Outcome: mustValue(run.NewSettlement(run.SettlementStatusSucceeded, []byte(`{}`)))}
		}, ErrInvalidProtocol},
		{"unrequested child exit", false, func(_ run.EffectID, _ run.WaitKey) run.Message {
			return run.Exit{Child: mustValue(testTurn(1).Process.Child(mustValue(run.ParseChildKey("foreign")))), Termination: mustValue(mustValue(run.ParsePayload([]byte(`{"output":null}`))).Decode[run.Termination]())}
		}, ErrInvalidProtocol},
		{"tool result masquerading as steering", false, func(_ run.EffectID, _ run.WaitKey) run.Message {
			return run.Signal{ID: mustValue(run.ParseSignalID("forged")), Payload: mustValue(run.ParsePayload(questionOutcome.Payload().JSON()))}
		}, ErrInvalidSteer},
		{"unrequested answer", false, func(_ run.EffectID, _ run.WaitKey) run.Message {
			return run.Signal{ID: mustValue(run.ParseSignalID("answer")), Wait: mustValue(run.ParseWaitKey("other")), Payload: mustValue(run.EncodePayload(true))}
		}, ErrInvalidProtocol},
		{"wrong question", true, func(_ run.EffectID, _ run.WaitKey) run.Message {
			return run.Signal{ID: mustValue(run.ParseSignalID("answer")), Wait: mustValue(run.ParseWaitKey("other")), Payload: mustValue(run.EncodePayload(true))}
		}, ErrInvalidProtocol},
		{"wrong answer type", true, func(_ run.EffectID, wait run.WaitKey) run.Message {
			return run.Signal{ID: mustValue(run.ParseSignalID("answer")), Wait: wait, Payload: mustValue(run.EncodePayload("not a boolean"))}
		}, ErrInvalidProtocol},
		{"completed effect reused while waiting", true, func(effect run.EffectID, _ run.WaitKey) run.Message {
			return run.Completion{Effect: effect, Outcome: questionOutcome}
		}, ErrInvalidProtocol},
	} {
		t.Run(row.name, func(t *testing.T) {
			execution, effect := pendingToolExecution(t, definition)
			var key run.WaitKey
			if row.question {
				transition := advance(t, execution, 4, run.Completion{Effect: effect, Outcome: questionOutcome})
				key = transition.Waits()[0].Key
			}
			before := mustValue(execution.Snapshot())
			turn := testTurn(5)
			turn.Messages = []run.Message{row.message(effect, key)}
			transition, err := execution.Step(t.Context(), turn)
			if !errors.Is(err, row.failure) || transition.Valid() {
				t.Fatal("unrelated message advanced a tool obligation", transition, err)
			}
			if after := mustValue(execution.Snapshot()); !bytes.Equal(before.Payload(), after.Payload()) {
				t.Fatal("rejected input changed the owned continuation")
			}
		})
	}
	execution, effect := pendingToolExecution(t, definition)
	transition := advance(t, execution, 4, run.Completion{Effect: effect, Outcome: mustValue(failedSettlement(errors.New("dispatcher refused")))})
	failure, found := transition.Failure()
	if !found || transition.Kind() != run.TransitionKindFail || transition.Consumed() != 1 || failure.Kind() != run.FailureKindContract || failure.Code() != failureCodeDispatchRejected || failure.Message() != "dispatcher refused" {
		t.Fatal("definite dispatch failure lost its committed classification")
	}
}
