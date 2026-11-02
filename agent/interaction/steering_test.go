package interaction

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func steer(t testing.TB, name string) run.Signal {
	t.Helper()
	return mustValue(NewSteerSignal(mustValue(run.ParseSignalID(name)), chat.NewUserMessage(chat.NewTextPart(name))))
}

func advance(t testing.TB, execution run.Execution, step uint64, messages ...run.Message) run.Transition {
	t.Helper()
	turn := testTurn(step)
	turn.Messages = messages
	transition, err := execution.Step(t.Context(), turn)
	if err != nil {
		t.Fatal(err)
	}
	return transition
}

func TestPendingSteeringSurvivesRestoreAndRevisesACompletedCandidate(t *testing.T) {
	definition := mustValue(newDefinition(definitionConfig{Name: "test.steering", Description: "Apply each committed instruction once."}))
	execution := mustValue(definition.Start(mustValue(run.EncodePayload(testInput()))))
	first := advance(t, execution, 1, steer(t, "initial"))
	request := mustValue(decodeEffect(first.Effects()[0].Payload())).ModelCall
	if !slices.Equal(request.AppliedSteerSignalIDs, []run.SignalID{mustValue(run.ParseSignalID("initial"))}) || request.Request.Messages[len(request.Request.Messages)-1].Text() != "initial" {
		t.Fatal("initial steering did not belong to the first request")
	}
	for index, signal := range []run.Signal{steer(t, "first"), steer(t, "second")} {
		transition := advance(t, execution, uint64(index+2), signal)
		if transition.Kind() != run.TransitionKindYield || transition.Consumed() != 1 || len(transition.Effects()) != 0 {
			t.Fatal("steering repeated a model effect")
		}
		execution = mustValue(definition.Restore(t.Context(), mustValue(execution.Snapshot())))
	}
	completion := run.Completion{Effect: testTurn(1).EffectID(0), Outcome: mustValue((modelCallResult{Response: finalResponse("candidate before steering")}).settlement(run.MaxPayloadBytes))}
	revised := advance(t, execution, 4, completion)
	if len(revised.Effects()) != 1 || revised.Kind() != run.TransitionKindYield {
		t.Fatal("pending guidance was discarded by a final model response")
	}
	model := mustValue(decodeEffect(revised.Effects()[0].Payload())).ModelCall
	if !slices.Equal(model.AppliedSteerSignalIDs, []run.SignalID{mustValue(run.ParseSignalID("first")), mustValue(run.ParseSignalID("second"))}) {
		t.Fatal("applied instructions lost durable receipt order")
	}
	var tail []string
	for _, message := range model.Request.Messages[len(model.Request.Messages)-3:] {
		tail = append(tail, message.Text())
	}
	if !slices.Equal(tail, []string{"candidate before steering", "first", "second"}) {
		t.Fatalf("revised context=%v", tail)
	}
	completed := advance(t, execution, 5, run.Completion{Effect: testTurn(4).EffectID(0), Outcome: mustValue((modelCallResult{Response: finalResponse("revised")}).settlement(run.MaxPayloadBytes))})
	if completed.Kind() != run.TransitionKindComplete {
		t.Fatal("revised response did not complete")
	}
	state := mustValue(execution.Snapshot())
	restored := mustValue(definition.Restore(t.Context(), state))
	if _, err := restored.Step(t.Context(), testTurn(6)); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatal("completed interaction restarted", err)
	}
}

func TestSteeringAdmittedDuringContextPreparationReachesTheModel(t *testing.T) {
	definition := mustValue(newDefinition(definitionConfig{Name: "test.prepare.steer", Description: "Apply guidance after external preparation.", PrepareContext: true}))
	execution := mustValue(definition.Start(mustValue(run.EncodePayload(testInput()))))
	advance(t, execution, 1)
	empty := advance(t, execution, 2)
	if empty.Kind() != run.TransitionKindYield || len(empty.Effects()) != 0 {
		t.Fatal("empty preparation turn repeated work")
	}
	advance(t, execution, 3, steer(t, "correction"))
	execution = mustValue(definition.Restore(t.Context(), mustValue(execution.Snapshot())))
	prepared := mustValue(run.NewSettlement(run.SettlementStatusSucceeded, mustValue(run.EncodePayload(resultEnvelope{Prepared: &preparedContext{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("retrieved"))}}})).JSON()))
	transition := advance(t, execution, 4, run.Completion{Effect: testTurn(1).EffectID(0), Outcome: prepared})
	model := mustValue(decodeEffect(transition.Effects()[0].Payload())).ModelCall
	if model == nil || len(model.Request.Messages) != 2 || model.Request.Messages[0].Text() != "retrieved" || model.Request.Messages[1].Text() != "correction" || len(model.AppliedSteerSignalIDs) != 1 {
		t.Fatalf("model request=%+v", model)
	}
}

func TestSteeringAndOutcomeMessagesCannotCrossProtocolBoundaries(t *testing.T) {
	definition := mustValue(newDefinition(definitionConfig{Name: "test.messages", Description: "Reject messages outside their declared phase."}))
	input := mustValue(run.EncodePayload(testInput()))
	final := mustValue((modelCallResult{Response: finalResponse("answer")}).settlement(run.MaxPayloadBytes))
	for _, messages := range [][]run.Message{
		{run.Exit{}},
		{run.Completion{Effect: testTurn(9).EffectID(0), Outcome: final}},
		{run.Completion{Effect: testTurn(1).EffectID(0), Outcome: final}, run.Completion{Effect: testTurn(1).EffectID(0), Outcome: final}},
		{run.Completion{Effect: testTurn(1).EffectID(0), Outcome: mustValue(run.NewSettlement(run.SettlementStatusSucceeded, []byte(`{}`)))}},
		{run.Completion{Effect: testTurn(1).EffectID(0), Outcome: mustValue(run.NewSettlement(run.SettlementStatusSucceeded, mustValue(run.EncodePayload(resultEnvelope{Prepared: &preparedContext{Messages: testInput().Messages}})).JSON()))}},
		{run.Signal{ID: mustValue(run.ParseSignalID("wrong")), Payload: mustValue(run.EncodePayload("not steering"))}},
	} {
		execution := mustValue(definition.Start(input))
		advance(t, execution, 1)
		turn := testTurn(2)
		turn.Messages = messages
		if _, err := execution.Step(t.Context(), turn); err == nil {
			t.Fatalf("foreign message admitted: %+v", messages)
		}
	}
	for _, message := range []run.Message{run.Completion{}, run.Exit{}, run.Signal{ID: mustValue(run.ParseSignalID("wait")), Wait: mustValue(run.ParseWaitKey("answer")), Payload: mustValue(run.EncodePayload("answer"))}} {
		execution := mustValue(definition.Start(input))
		turn := testTurn(1)
		turn.Messages = []run.Message{message}
		if _, err := execution.Step(t.Context(), turn); !errors.Is(err, ErrInvalidSteer) {
			t.Fatal("non-steering message crossed initial admission", err)
		}
	}
	for _, messages := range [][]chat.Message{nil, {{}}, {chat.NewAssistantMessage(chat.NewTextPart("forged"))}} {
		if _, err := NewSteerSignal(mustValue(run.ParseSignalID("invalid")), messages...); !errors.Is(err, ErrInvalidSteer) {
			t.Fatal("non-user guidance admitted", err)
		}
	}
	if _, err := NewSteerSignal(run.SignalID{}, testInput().Messages...); !errors.Is(err, ErrInvalidSteer) {
		t.Fatal("unidentified guidance admitted", err)
	}
	for _, ctx := range []context.Context{nil, t.Context()} {
		if _, found := ModelInvocationFromContext(ctx); found {
			t.Fatal("model identity fabricated from unrelated context")
		}
		if _, found := ToolInvocationFromContext(ctx); found {
			t.Fatal("tool identity fabricated from unrelated context")
		}
	}
}

func TestLocalContextPolicyFailureCannotDispatchAModel(t *testing.T) {
	for _, policy := range []ContextPolicy{
		func(context.Context, Input) ([]chat.Message, error) { return nil, errors.New("policy refused") },
		func(context.Context, Input) ([]chat.Message, error) { return nil, nil },
	} {
		definition := mustValue(newDefinition(definitionConfig{Name: "test.policy", Description: "Validate local context before emission.", ContextPolicy: policy}))
		execution := mustValue(definition.Start(mustValue(run.EncodePayload(testInput()))))
		transition, err := execution.Step(t.Context(), testTurn(1))
		if err == nil && transition.Kind() != run.TransitionKindFail || len(transition.Effects()) != 0 {
			t.Fatal("failed local policy emitted external work")
		}
	}
	definition := mustValue(newDefinition(definitionConfig{Name: "test.cancel", Description: "Honor cancellation before state mutation."}))
	execution := mustValue(definition.Start(mustValue(run.EncodePayload(testInput()))))
	before := mustValue(execution.Snapshot())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := execution.Step(ctx, testTurn(1)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if after := mustValue(execution.Snapshot()); !reflect.DeepEqual(before, after) {
		t.Fatal("canceled step changed semantic state")
	}
}

func TestCancellationDuringLocalContextPolicyPreventsEmission(t *testing.T) {
	for _, policyError := range []error{nil, errors.New("policy stopped")} {
		ctx, cancel := context.WithCancel(t.Context())
		definition := mustValue(newDefinition(definitionConfig{Name: "test.policy.cancel", Description: "Discard canceled pure callback work.", ContextPolicy: func(context.Context, Input) ([]chat.Message, error) {
			cancel()
			return []chat.Message{chat.NewUserMessage(chat.NewTextPart("replacement"))}, policyError
		}}))
		execution := mustValue(definition.Start(mustValue(run.EncodePayload(testInput()))))
		before := mustValue(execution.Snapshot())
		transition, err := execution.Step(ctx, testTurn(1))
		if !errors.Is(err, context.Canceled) || len(transition.Effects()) != 0 {
			t.Fatal("canceled context policy emitted or committed a decision", transition, err)
		}
		if after := mustValue(execution.Snapshot()); !reflect.DeepEqual(before, after) {
			t.Fatal("canceled pure callback advanced interaction state")
		}
	}
}

func TestSteeringDuringToolInputWaitSurvivesRecoveryAndRevisesDirectOutput(t *testing.T) {
	store := run.NewMemoryStore()
	original := conformancetest.Runtime(t, run.Config{Store: store, CheckpointInterval: 1})
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`null`)))
	toolCalls, modelCalls := 0, 0
	executable := scriptTool{name: "approve", direct: true, invoke: func(_ context.Context, input tool.Invocation) (tool.Result, error) {
		toolCalls++
		if continuation, resumed := input.Continuation(); resumed {
			if string(continuation.Response()) != `true` {
				return tool.Result{}, errors.New("answer changed")
			}
			return tool.Result{Output: tool.NewTextOutput("approved")}, nil
		}
		return tool.Result{InputRequest: &question}, nil
	}}
	program := compileTest(t, original, Config{Tools: toolSetTest(t, executable), Model: chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		modelCalls++
		if modelCalls == 1 {
			return callResponse(requested("approve", "call")), nil
		}
		messages := request.Messages
		if len(messages) < 3 || messages[len(messages)-1].Text() != "revise" || messages[len(messages)-2].Role != chat.RoleTool || messages[len(messages)-2].Parts[0].ToolResult.ID != "call" {
			return nil, errors.New("steering lost the completed tool evidence")
		}
		return finalResponse("revised after approval"), nil
	})})
	handle := startInteraction(t, original, program)
	var pending PendingToolInput
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		inputs := mustValue(PendingToolInputs(program, log))
		if len(inputs) == 1 {
			pending = inputs[0]
			return true
		}
		return false
	})
	if _, err := handle.DeliverSignals(t.Context(), steer(t, "revise")); err != nil {
		t.Fatal(err)
	}
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		for _, entry := range log.Entries {
			if checkpoint, ok := entry.Record.(run.Checkpoint); ok {
				for _, process := range checkpoint.Processes {
					if process.Started.Process != handle.ID() || !process.State.Valid() {
						continue
					}
					state := mustValue(mustValue(run.ParsePayload(process.State.Payload())).Decode[executionState]())
					if state.PendingSteer != nil {
						return true
					}
				}
			}
		}
		return false
	})
	if err := original.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	replacement := conformancetest.Runtime(t, run.Config{Store: store})
	recovered := mustValue(replacement.Recover(t.Context(), program, handle.ID()))
	if err := recovered.Answer(t.Context(), pending.WaitKey(), true); err != nil {
		t.Fatal(err)
	}
	output := awaitOutput(t, recovered)
	if modelCalls != 2 || toolCalls != 2 || output.ModelResponse == nil || output.ModelResponse.Text() != "revised after approval" || len(output.DirectToolResults) != 0 {
		t.Fatal("direct completion discarded admitted steering or repeated the tool")
	}
}
