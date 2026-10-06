package interaction

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

type fuzzDelegateInput struct {
	Task string `json:"task"`
}

type fuzzDelegateOutput struct {
	Result string `json:"result"`
}

type fuzzDispatcher struct{}

func (fuzzDispatcher) Dispatch(context.Context, agent.EffectRequest, agent.DeltaEmitter) (agent.Settlement, error) {
	return agent.Settlement{}, errors.New("fuzz deployment does not dispatch effects")
}

func (fuzzDispatcher) Policy(agent.Effect) agent.EffectPolicy {
	return agent.EffectPolicy{Replay: agent.ReplayPolicyNever}
}

func FuzzExecutionStateRestore(f *testing.F) {
	definition := fuzzInteractionDefinition(f)
	for _, state := range fuzzInteractionStates(f, definition) {
		f.Add([]byte(state.Payload()))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"model_call_count":1}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		state, err := agent.ParseExecutionState(executionStateKind, payload)
		if err != nil {
			return
		}
		execution, err := definition.Restore(t.Context(), state)
		if err != nil {
			return
		}
		captured, err := execution.Snapshot()
		if err != nil {
			t.Fatalf("restored state cannot be captured: %v", err)
		}
		restored, err := definition.Restore(t.Context(), captured)
		if err != nil {
			t.Fatalf("captured state cannot be restored: %v", err)
		}
		roundTrip, err := restored.Snapshot()
		if err != nil {
			t.Fatalf("round-trip state cannot be captured: %v", err)
		}
		if !bytes.Equal(captured.Payload(), roundTrip.Payload()) {
			t.Fatalf("state round trip changed payload\nfirst:  %s\nsecond: %s", captured.Payload(), roundTrip.Payload())
		}
	})
}

func TestRestoreValidatesFinishReasonInPendingRound(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	for _, seed := range fuzzInteractionStates(t, definition) {
		var state executionState
		if err := jsonv2.Unmarshal(seed.Payload(), &state); err != nil {
			t.Fatal(err)
		}
		if state.ToolRound == nil {
			continue
		}
		for _, reason := range []chat.FinishReason{chat.FinishReasonStop, chat.FinishReasonLength, chat.FinishReasonContentFilter, chat.FinishReasonRefusal, chat.FinishReasonOther} {
			t.Run(fmt.Sprintf("phase_%d/%s", state.phase(), reason), func(t *testing.T) {
				state.ToolRound.Response.Output.FinishReason = reason
				payload, err := jsonv2.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				captured, err := agent.ParseExecutionState(executionStateKind, payload)
				if err != nil {
					t.Fatal(err)
				}
				_, restoreErr := definition.Restore(t.Context(), captured)
				validRejection := state.phase() == phaseRoundComplete && reason == chat.FinishReasonLength
				if validRejection && restoreErr != nil || !validRejection && !errors.Is(restoreErr, ErrInvalidExecutionState) {
					t.Fatalf("Restore error = %v, valid rejection = %t", restoreErr, validRejection)
				}
			})
		}
	}
}

func TestRestoreKeepsCompletionWithoutItsOutput(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	for _, test := range []struct {
		name  string
		calls uint64
		valid bool
	}{
		{name: "completed", calls: 1, valid: true},
		{name: "no model call"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, err := (executionState{
				ModelCallCount: test.calls,
				WorkingContext: &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}},
				Completed:      true,
			}).snapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := definition.Restore(t.Context(), state)
			if !test.valid {
				if !errors.Is(err, ErrInvalidExecutionState) {
					t.Fatalf("Restore = %v, want ErrInvalidExecutionState", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			captured, err := restored.Snapshot()
			if err != nil || !bytes.Equal(state.Payload(), captured.Payload()) {
				t.Fatalf("completion changed after restoration: %v", err)
			}
		})
	}
}

func fuzzInteractionDefinition(f testing.TB) *Definition {
	f.Helper()
	inputSchema, err := agent.SchemaFor[fuzzDelegateInput]()
	if err != nil {
		f.Fatal(err)
	}
	outputSchema, err := agent.SchemaFor[fuzzDelegateOutput]()
	if err != nil {
		f.Fatal(err)
	}
	workerDefinition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.fuzz_worker", Description: "Provide a deterministic fuzz worker contract.",
		MaxModelCalls: agent.NewQuota(1),
	})
	if err != nil {
		f.Fatal(err)
	}
	workerDeployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: workerDefinition, Dispatcher: fuzzDispatcher{},
		ImplementationDigest: agent.ComputeDigest([]byte("interaction-fuzz-worker-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("interaction-fuzz-worker-configuration")),
	})
	if err != nil {
		f.Fatal(err)
	}
	budget := agent.Budget{Steps: agent.NewQuota(10), Effects: agent.NewQuota(10), Signals: agent.NewQuota(10)}
	delegate := Delegate{
		definition: chat.ToolDefinition{
			Name: "delegate_fuzz", Description: "Delegate one fuzz task to the exact worker.",
			InputSchema: inputSchema.JSON(),
		},
		deployment: workerDeployment, inputSchema: inputSchema, outputSchema: outputSchema,
		budget: budget, capabilities: agent.CapabilitySet{},
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.fuzz", Description: "Exercise strict Interaction state restoration.",
		MaxModelCalls: agent.NewQuota(4), Delegates: []Delegate{delegate},
	})
	if err != nil {
		f.Fatal(err)
	}
	return definition
}

func fuzzInteractionStates(f testing.TB, definition *Definition) []agent.ExecutionState {
	f.Helper()
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("fuzz"))}}
	call := chat.ToolCall{ID: "call_fuzz", Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
	message := chat.NewAssistantMessage(chat.NewToolCallPart(call))
	response := &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}
	processID, _ := agent.ParseProcessID("process:fuzz-child")
	waitID, _ := agent.ParseWaitID("wait:fuzz-child")
	steerSignalID, _ := agent.ParseSignalID("signal:fuzz-steer")
	artifactOutput, _ := agent.EncodePayload(fuzzDelegateOutput{Result: "settled"})
	states := []executionState{
		{
			WorkingContext: request.Clone(), ModelCallCount: 1,
			ToolRound: &toolCallRound{Response: response.Clone(), Results: []toolCallResult{{
				Disposition: ResultRejected, Output: chat.NewTextToolOutput("worker unavailable"),
			}}},
		},
		{
			WorkingContext: request.Clone(), ModelCallCount: 1,
			ToolRound: &toolCallRound{Response: response.Clone(),
				ChildBatch: &childCallBatch{Kind: childCallsDelegate, Invocations: []*childInvocationState{{}}}},
		},
		{
			WorkingContext: request.Clone(), ModelCallCount: 1,
			PendingSteer: &steerBatch{
				Messages:  []chat.Message{chat.NewUserMessage(chat.NewTextPart("fuzz steer"))},
				SignalIDs: []agent.SignalID{steerSignalID},
			},
			ToolRound: &toolCallRound{Response: response.Clone(), ChildBatch: &childCallBatch{Kind: childCallsDelegate, WaitID: &waitID, Invocations: []*childInvocationState{{
				ProcessID: &processID,
			}}}},
		},
		{
			WorkingContext: request.Clone(), ModelCallCount: 2,
			ArtifactRecords: []artifactRecord{{
				ModelCallSequence: 1, ToolCallIndex: 0, ToolCallID: "call_settled",
				DelegateName: "delegate_fuzz", Output: artifactOutput,
			}},
		},
	}
	encoded := make([]agent.ExecutionState, 0, len(states))
	for _, state := range states {
		if err := state.validate(f.Context(), definition); err != nil {
			f.Fatal(err)
		}
		payload, err := jsonv2.Marshal(state)
		if err != nil {
			f.Fatal(err)
		}
		envelope, err := agent.ParseExecutionState(executionStateKind, payload)
		if err != nil {
			f.Fatal(err)
		}
		encoded = append(encoded, envelope)
	}
	return encoded
}
