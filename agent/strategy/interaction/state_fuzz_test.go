package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func (fuzzDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicyNever
}

func FuzzExecutionStateRestore(f *testing.F) {
	definition := fuzzInteractionDefinition(f)
	for _, state := range fuzzInteractionStates(f, definition) {
		f.Add([]byte(state.Payload()))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"phase":"waiting_children"}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		state, err := agent.NewExecutionState(executionStateKind, payload)
		if err != nil {
			return
		}
		execution, err := definition.Restore(state)
		if err != nil {
			return
		}
		captured, err := execution.Snapshot()
		if err != nil {
			t.Fatalf("restored state cannot be captured: %v", err)
		}
		restored, err := definition.Restore(captured)
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

func TestRestoreRejectsNonToolCompletionInPendingBatch(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	for _, seed := range fuzzInteractionStates(t, definition) {
		var state executionState
		if err := json.Unmarshal(seed.Payload(), &state); err != nil {
			t.Fatal(err)
		}
		if state.ToolRound == nil {
			continue
		}
		for _, reason := range []chat.FinishReason{chat.FinishReasonStop, chat.FinishReasonLength, chat.FinishReasonContentFilter, chat.FinishReasonRefusal, chat.FinishReasonOther} {
			t.Run(string(state.Phase)+"/"+reason.String(), func(t *testing.T) {
				state.ToolRound.Response.Output.FinishReason = reason
				payload, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				captured, err := agent.NewExecutionState(executionStateKind, payload)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := definition.Restore(captured); !errors.Is(err, ErrInvalidExecutionState) {
					t.Fatalf("Restore error = %v, want ErrInvalidExecutionState", err)
				}
			})
		}
	}
}

func TestRestoreRequiresOneCompletedResult(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	message := chat.NewAssistantMessage(chat.NewTextPart("done"))
	response := &chat.Response{Output: &chat.Output{
		Message: &message, FinishReason: chat.FinishReasonStop,
	}}
	results := []chat.ToolResult{{ID: "call", Name: "direct", Output: chat.NewTextToolOutput("done")}}
	for _, test := range []struct {
		name     string
		response *chat.Response
		results  []chat.ToolResult
		calls    uint32
		valid    bool
	}{
		{name: "model", response: response, calls: 2, valid: true},
		{name: "direct tools", results: results, calls: 1, valid: true},
		{name: "missing result", calls: 1},
		{name: "competing results", response: response, results: results, calls: 1},
		{name: "no model call", response: response},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := &Output{Source: CompletionSourceDirectToolResults, ModelResponse: test.response, DirectToolResults: test.results, ModelCalls: test.calls}
			if test.response != nil {
				output.Source = CompletionSourceModelResponse
			}
			state, err := (executionState{
				Phase: phaseCompleted, ModelCallCount: test.calls,
				WorkingContext: &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}},
				FinalOutput:    output,
			}).snapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := definition.Restore(state)
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
				t.Fatalf("completed result changed after restoration: %v", err)
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
		MaxModelCalls: 1,
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
	reference := workerDeployment.DeploymentRef()
	budget := agent.Budget{Steps: 10, Effects: 10, Signals: 10}
	delegate := Delegate{
		definition: chat.ToolDefinition{
			Name: "delegate_fuzz", Description: "Delegate one fuzz task to the exact worker.",
			InputSchema: inputSchema.JSON(),
		},
		deploymentRef: reference, inputSchema: inputSchema, outputSchema: outputSchema,
		budget: budget, capabilities: agent.CapabilitySet{},
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.fuzz", Description: "Exercise strict Interaction state restoration.",
		MaxModelCalls: 4, Delegates: []Delegate{delegate},
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
	key, _ := DelegateChildKey(1, call)
	processID, _ := agent.ParseProcessID("process:fuzz-child")
	waitID, _ := agent.ParseWaitID("wait:fuzz-child")
	steerSignalID, _ := agent.ParseSignalID("signal:fuzz-steer")
	artifactOutput, _ := agent.EncodeOutput(fuzzDelegateOutput{Result: "settled"})
	states := []executionState{
		{
			Phase: phaseAwaitingChildStarts, WorkingContext: request.Clone(), ModelCallCount: 1,
			ToolRound: &toolCallRound{Response: response.Clone(),
				ChildBatch: &childCallBatch{Kind: childCallsDelegate, NextStartIndex: 1, Invocations: []childInvocationState{{ChildKey: &key}}}},
		},
		{
			Phase: phaseWaitingChildren, WorkingContext: request.Clone(), ModelCallCount: 1,
			PendingSteer: &steerBatch{
				Messages:  []chat.Message{chat.NewUserMessage(chat.NewTextPart("fuzz steer"))},
				SignalIDs: []agent.SignalID{steerSignalID},
			},
			ToolRound: &toolCallRound{Response: response.Clone(), ChildBatch: &childCallBatch{Kind: childCallsDelegate, NextStartIndex: 1, WaitID: &waitID, Invocations: []childInvocationState{{
				ChildKey: &key, ProcessID: &processID,
			}}}},
		},
		{
			Phase: phaseAwaitingModel, WorkingContext: request.Clone(), ModelCallCount: 2,
			ArtifactRecords: []artifactRecord{{
				ModelCallSequence: 1, ToolCallIndex: 0, ToolCallID: "call_settled",
				DelegateName: "delegate_fuzz", Output: artifactOutput,
			}},
		},
	}
	encoded := make([]agent.ExecutionState, 0, len(states))
	for _, state := range states {
		if err := state.validate(definition); err != nil {
			f.Fatal(err)
		}
		payload, err := json.Marshal(state)
		if err != nil {
			f.Fatal(err)
		}
		envelope, err := agent.NewExecutionState(executionStateKind, payload)
		if err != nil {
			f.Fatal(err)
		}
		encoded = append(encoded, envelope)
	}
	return encoded
}
