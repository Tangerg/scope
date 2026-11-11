package interaction

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestDelegateArtifactsRestoreWithTheOriginalCallOrder(t *testing.T) {
	delegate := compiledDelegate(t, conformancetest.Compiler(t), "worker", func(_ context.Context, value delegateValue) (delegateValue, error) {
		return delegateValue{Value: value.Value * 2}, nil
	})
	definition := mustValue(newDefinition(Config{Name: "test.artifacts", Description: "Restore ordered typed evidence.", Delegates: []Delegate{delegate}, CompletionValidator: func(_ context.Context, candidate CompletionCandidate) (CompletionDecision, error) {
		artifacts := candidate.Artifacts()
		if len(artifacts) != 2 {
			return CompletionDecision{}, errors.New("missing delegated evidence")
		}
		for index, artifact := range artifacts {
			value, err := artifact.Output().Decode[delegateValue]()
			if err != nil || value.Value != (index+1)*2 || artifact.ModelCallSequence() != 1 || artifact.DelegateName() != "worker" || artifact.ToolCallID() != []string{"first", "second"}[index] {
				return CompletionDecision{}, errors.New("artifact order or identity changed")
			}
		}
		artifacts[0] = Artifact{}
		if candidate.Artifacts()[0].ToolCallID() != "first" {
			return CompletionDecision{}, errors.New("artifact projection aliases state")
		}
		return CompletionDecision{Accepted: true}, nil
	}}))
	models := 0
	dispatcher := mustValue(newDispatcher(definition, Config{Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		models++
		if models == 1 {
			return callResponse(chat.ToolCall{ID: "first", Name: "worker", Arguments: `{"value":1}`}, chat.ToolCall{ID: "second", Name: "worker", Arguments: `{"value":2}`}), nil
		}
		return finalResponse("evidence complete"), nil
	}))}))
	result := conformancetest.Run(t, run.BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "artifacts"}, run.Config{}, mustValue(run.EncodePayload(testInput())))
	output := mustValue(result.Output())
	value := mustValue(output.Decode[Output]())
	if value.ModelResponse.Text() != "evidence complete" || value.ModelCalls != 2 {
		t.Fatalf("output=%+v", value)
	}
}

func TestRestoreRejectsContradictoryInteractionOwnership(t *testing.T) {
	delegate := compiledDelegate(t, conformancetest.Compiler(t), "worker", func(_ context.Context, value delegateValue) (delegateValue, error) { return value, nil })
	tools := toolSetTest(t, scriptTool{name: "echo", classify: func(tool.Invocation) (string, bool) { return "", true }})
	definition := mustValue(newDefinition(Config{Name: "test.restore", Description: "Reject contradictory continuation facts.", Tools: tools, Delegates: []Delegate{delegate}, MaxModelCalls: run.NewQuota(4)}))
	call := requested("echo", "call")
	second := requested("echo", "second")
	reference := testTurn(3).EffectID(0)
	otherReference := testTurn(3).EffectID(1)
	foreign := run.EffectID{Process: mustValue(run.ParseProcessID("foreign")), Step: 3}
	request := mustValue(newInputRequest([]byte(`"approve"`), []byte(`{"type":"boolean"}`), []byte(`null`)))
	steerID := mustValue(run.ParseSignalID("guidance"))
	validArtifact := artifactRecord{ModelCallSequence: 1, ToolCallID: "evidence", DelegateName: "worker", Output: mustValue(run.EncodePayload(delegateValue{Value: 1}))}
	state := func() executionState {
		return executionState{WorkingContext: new(testInput()), ModelCallCount: 1, PendingEffect: new(testTurn(1).EffectID(0))}
	}
	round := func() executionState {
		return executionState{WorkingContext: new(testInput()), ModelCallCount: 1, ToolRound: &toolCallRound{Response: callResponse(call), Batch: &callBatch{Kind: callTool, Invocations: []*callInvocation{{Reference: reference, Pending: &reference}}}}}
	}
	for _, row := range []struct {
		name   string
		base   func() executionState
		change func(*executionState)
	}{
		{"missing pending model", state, func(value *executionState) { value.PendingEffect = nil }},
		{"invalid pending identity", state, func(value *executionState) { value.PendingEffect = new(run.EffectID) }},
		{"missing working context", state, func(value *executionState) { value.WorkingContext = nil }},
		{"invalid working context", state, func(value *executionState) { value.WorkingContext = new(Input) }},
		{"past model quota", state, func(value *executionState) { value.ModelCallCount = 5 }},
		{"unbound advertisements", state, func(value *executionState) { value.AdvertisedToolNames = []string{"missing"} }},
		{"repeated advertisements", state, func(value *executionState) { value.AdvertisedToolNames = []string{"echo", "echo"} }},
		{"unidentified steering", state, func(value *executionState) { value.PendingSteer = &steerBatch{Messages: testInput().Messages} }},
		{"repeated steering", state, func(value *executionState) {
			value.PendingSteer = &steerBatch{Messages: testInput().Messages, SignalIDs: []run.SignalID{steerID, steerID}}
		}},
		{"fresh state with unapplied steering", state, func(value *executionState) {
			value.ModelCallCount = 0
			value.PendingEffect = nil
			value.PendingSteer = &steerBatch{Messages: testInput().Messages, SignalIDs: []run.SignalID{steerID}}
		}},
		{"undeclared preparation", state, func(value *executionState) { value.Preparation = new(contextPreparation) }},
		{"unknown artifact owner", state, func(value *executionState) {
			artifact := validArtifact
			artifact.DelegateName = "missing"
			value.ArtifactRecords = []artifactRecord{artifact}
		}},
		{"artifact beyond call count", state, func(value *executionState) {
			artifact := validArtifact
			artifact.ModelCallSequence = 2
			value.ArtifactRecords = []artifactRecord{artifact}
		}},
		{"artifact violates child schema", state, func(value *executionState) {
			artifact := validArtifact
			artifact.Output = mustValue(run.EncodePayload("wrong"))
			value.ArtifactRecords = []artifactRecord{artifact}
		}},
		{"repeated artifact", state, func(value *executionState) { value.ArtifactRecords = []artifactRecord{validArtifact, validArtifact} }},
		{"artifact order reversed", state, func(value *executionState) {
			value.ModelCallCount = 2
			later := validArtifact
			later.ModelCallSequence = 2
			value.ArtifactRecords = []artifactRecord{later, validArtifact}
		}},
		{"missing model proposal", round, func(value *executionState) { value.ToolRound.Response = nil }},
		{"unadmitted arguments in later proposal", round, func(value *executionState) {
			value.ToolRound.Batch = nil
			invalid := second
			invalid.Arguments = `{"undeclared":true}`
			value.ToolRound.Response = callResponse(call, invalid)
		}},
		{"unbound later proposal", round, func(value *executionState) {
			value.ToolRound.Batch = nil
			value.ToolRound.Response = callResponse(call, requested("missing", "unbound"))
		}},
		{"partially rejected truncated proposal", round, func(value *executionState) {
			value.ToolRound.Batch = nil
			value.ToolRound.Response = callResponse(call, second)
			value.ToolRound.Response.Output.FinishReason = chat.FinishReasonLength
			value.ToolRound.Results = []toolCallResult{{Disposition: ResultRejected}}
		}},
		{"unrequested call", round, func(value *executionState) { value.ToolRound.Response = finalResponse("finished") }},
		{"unfinished call proposal", round, func(value *executionState) { value.ToolRound.Response.Output.FinishReason = chat.FinishReasonStop }},
		{"truncated call dispatched", round, func(value *executionState) { value.ToolRound.Response.Output.FinishReason = chat.FinishReasonLength }},
		{"call without model", round, func(value *executionState) { value.ModelCallCount = 0 }},
		{"missing invocation", round, func(value *executionState) { value.ToolRound.Batch.Invocations = nil }},
		{"empty invocation", round, func(value *executionState) {
			value.ToolRound.Batch.Invocations[0] = &callInvocation{Reference: reference}
		}},
		{"unknown call kind", round, func(value *executionState) { value.ToolRound.Batch.Kind = "unknown" }},
		{"delegate kind for ordinary tool", round, func(value *executionState) {
			value.ToolRound.Batch.Kind = callDelegate
			value.ToolRound.Batch.Invocations[0] = &callInvocation{Reference: reference, Child: &child.Invocation{Request: &reference}}
		}},
		{"two owners of one obligation", round, func(value *executionState) {
			value.ToolRound.Batch.Invocations[0].Input = &inputWait{Effect: reference, Request: request}
		}},
		{"pending from foreign process", round, func(value *executionState) { value.ToolRound.Batch.Invocations[0].Pending = &foreign }},
		{"same-step continuation changes identity", round, func(value *executionState) { value.ToolRound.Batch.Invocations[0].Pending = &otherReference }},

		{"unstarted calls only", round, func(value *executionState) { value.ToolRound.Batch.Invocations[0] = nil }},
		{"unstarted prefix before dispatched suffix", round, func(value *executionState) {
			value.ToolRound.Response = callResponse(call, second)
			value.ToolRound.Batch.Invocations = []*callInvocation{nil, {Reference: otherReference, Pending: &otherReference}}
		}},
		{"duplicate logical reference", round, func(value *executionState) {
			value.ToolRound.Response = callResponse(call, second)
			value.ToolRound.Batch.Invocations = append(value.ToolRound.Batch.Invocations, &callInvocation{Reference: reference, Pending: &reference})
		}},
		{"too many concurrent obligations", round, func(value *executionState) {
			value.ToolRound.Response = callResponse(call, second)
			value.ToolRound.Batch.Invocations = append(value.ToolRound.Batch.Invocations, &callInvocation{Reference: otherReference, Pending: &otherReference})
		}},
		{"result without disposition", round, func(value *executionState) {
			value.ToolRound.Batch.Invocations[0] = &callInvocation{Reference: reference, Result: new(toolCallResult)}
		}},
		{"finished batch retained", round, func(value *executionState) {
			value.ToolRound.Batch.Invocations[0] = &callInvocation{Reference: reference, Result: &toolCallResult{Disposition: ResultSucceeded}}
		}},
		{"archived result still owns advertisement", round, func(value *executionState) {
			value.ToolRound.Batch = nil
			value.ToolRound.Results = []toolCallResult{{Disposition: ResultSucceeded, AdvertisedToolNames: []string{"echo"}}}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			value := row.base()
			row.change(&value)
			encoded, err := value.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := definition.Restore(t.Context(), encoded); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("contradictory state was admitted or lost its classification: %v", err)
			}
		})
	}
}

func TestRestoredDelegateBatchMustRetainItsDeclaredChildOwners(t *testing.T) {
	delegate := compiledDelegate(t, conformancetest.Compiler(t), "worker", func(_ context.Context, value delegateValue) (delegateValue, error) { return value, nil })
	definition := mustValue(newDefinition(Config{Name: "test.restore.children", Description: "Bind restored obligations to their declared calls.", Delegates: []Delegate{delegate}}))
	firstCall := chat.ToolCall{ID: "first", Name: "worker", Arguments: `{"value":1}`}
	secondCall := chat.ToolCall{ID: "second", Name: "worker", Arguments: `{"value":2}`}
	firstRef, secondRef := testTurn(3).EffectID(0), testTurn(3).EffectID(1)
	secondProcess := mustValue(secondRef.Process.Child(mustValue(DelegateChildKey(1, secondCall))))
	base := func() executionState {
		return executionState{WorkingContext: new(testInput()), ModelCallCount: 1, ToolRound: &toolCallRound{
			Response: callResponse(firstCall, secondCall), Batch: &callBatch{Kind: callDelegate, Invocations: []*callInvocation{
				{Reference: firstRef, Child: &child.Invocation{Request: &firstRef}},
				{Reference: secondRef, Child: &child.Invocation{Request: &secondRef}},
			}},
		}}
	}
	if _, err := definition.Restore(t.Context(), mustValue(base().snapshot())); err != nil {
		t.Fatal("valid pending delegation was rejected", err)
	}
	for _, row := range []struct {
		name   string
		change func(*executionState)
	}{
		{"undeclared child key", func(state *executionState) {
			process := mustValue(firstRef.Process.Child(mustValue(run.ParseChildKey("unrelated"))))
			state.ToolRound.Batch.Invocations[0].Child = &child.Invocation{Process: &process}
		}},
		{"another call's child", func(state *executionState) {
			state.ToolRound.Batch.Invocations[0].Child = &child.Invocation{Process: &secondProcess}
		}},
		{"spawn index differs from call slot", func(state *executionState) {
			changed := testTurn(3).EffectID(2)
			state.ToolRound.Batch.Invocations[0] = &callInvocation{Reference: changed, Child: &child.Invocation{Request: &changed}}
		}},
		{"spawns claim separate steps", func(state *executionState) {
			changed := testTurn(4).EffectID(1)
			state.ToolRound.Batch.Invocations[1] = &callInvocation{Reference: changed, Child: &child.Invocation{Request: &changed}}
		}},
		{"successful result violates child output", func(state *executionState) {
			state.ToolRound.Batch.Invocations[0] = &callInvocation{Reference: firstRef, Result: &toolCallResult{Disposition: ResultSucceeded, Output: tool.Output{Details: []byte(`"wrong child shape"`)}}}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			state := base()
			row.change(&state)
			if _, err := definition.Restore(t.Context(), mustValue(state.snapshot())); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatal("restored delegation acquired another call's ownership", err)
			}
		})
	}
}

func TestRestoredContinuationCannotOwnAnotherCallsOriginalEffect(t *testing.T) {
	tools := toolSetTest(t, scriptTool{name: "echo", classify: func(tool.Invocation) (string, bool) { return "", true }})
	definition := mustValue(newDefinition(Config{Name: "test.restore.continuation", Description: "Keep logical and resumed calls disjoint.", Tools: tools, MaxConcurrentToolCalls: 2}))
	first, second, third := testTurn(3).EffectID(0), testTurn(3).EffectID(1), testTurn(4).EffectID(0)
	continued := testTurn(6).EffectID(0)
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`null`)))
	for _, awaitingInput := range []bool{false, true} {
		state := executionState{WorkingContext: new(testInput()), ModelCallCount: 1, ToolRound: &toolCallRound{
			Response: callResponse(requested("echo", "first"), requested("echo", "second"), requested("echo", "third")),
			Batch: &callBatch{Kind: callTool, Invocations: []*callInvocation{
				{Reference: first, Pending: &continued},
				{Reference: second, Result: &toolCallResult{Disposition: ResultSucceeded}},
				{Reference: third, Result: &toolCallResult{Disposition: ResultSucceeded}},
			}},
		}}
		invocation := state.ToolRound.Batch.Invocations[0]
		if awaitingInput {
			invocation.Pending = nil
			invocation.Input = &inputWait{Effect: continued, Request: question}
		}
		if _, err := definition.Restore(t.Context(), mustValue(state.snapshot())); err != nil {
			t.Fatal("valid partially completed batch was rejected", err)
		}
		if awaitingInput {
			invocation.Input.Effect = third
		} else {
			invocation.Pending = &third
		}
		if _, err := definition.Restore(t.Context(), mustValue(state.snapshot())); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatal("continuation claimed another call's original effect", err)
		}
	}
}

func TestRestorePreservesProviderOrderOfDirectToolEffects(t *testing.T) {
	for _, row := range []struct {
		name        string
		concurrency int
	}{
		{"concurrent calls", 3},
		{"limited concurrency", 2},
	} {
		t.Run(row.name, func(t *testing.T) {
			tools := toolSetTest(t, scriptTool{name: "echo", classify: func(tool.Invocation) (string, bool) { return "", true }})
			definition := mustValue(newDefinition(Config{Name: "test.restore.order", Description: "Bind results to the original provider order.", Tools: tools, MaxConcurrentToolCalls: row.concurrency}))
			execution := mustValue(definition.Start(mustValue(run.EncodePayload(testInput()))))
			advance(t, execution, 1)
			modelOutcome := mustValue((modelCallResult{Response: callResponse(requested("echo", "first"), requested("echo", "second"), requested("echo", "third"))}).settlement())
			advance(t, execution, 2, run.Completion{Effect: testTurn(1).EffectID(0), Outcome: modelOutcome})
			advance(t, execution, 3)
			if row.concurrency == 2 {
				toolOutcome := mustValue((toolDispatchResult{Completion: &toolCallResult{Disposition: ResultSucceeded, Output: tool.NewTextOutput("first result")}}).settlement())
				advance(t, execution, 4, run.Completion{Effect: testTurn(3).EffectID(0), Outcome: toolOutcome})
			}
			snapshot := mustValue(execution.Snapshot())
			if _, err := definition.Restore(t.Context(), snapshot); err != nil {
				t.Fatal("valid issued calls did not restore", err)
			}
			state := mustValue(mustValue(run.ParsePayload(snapshot.Payload())).Decode[executionState]())
			calls := state.ToolRound.Batch.Invocations
			calls[0].Reference, calls[2].Reference = calls[2].Reference, calls[0].Reference
			for _, call := range calls {
				if call.Pending != nil {
					call.Pending = new(call.Reference)
				}
			}
			if _, err := definition.Restore(t.Context(), mustValue(state.snapshot())); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatal("provider calls claimed each other's committed effects", err)
			}
		})
	}
}
