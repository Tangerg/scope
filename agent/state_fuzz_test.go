package agent

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

type fuzzDelegateInput struct {
	Task string `json:"task"`
}

type fuzzDelegateOutput struct {
	Result string `json:"result"`
}

type fuzzDispatcher struct{}

func (fuzzDispatcher) Dispatch(context.Context, run.EffectRequest, run.DeltaEmitter) (run.Settlement, error) {
	return run.Settlement{}, errors.New("fuzz deployment does not dispatch effects")
}

func (fuzzDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

func FuzzExecutionStateRestore(f *testing.F) {
	definition := fuzzInteractionDefinition(f)
	for _, state := range fuzzInteractionStates(f, definition) {
		f.Add([]byte(state.Payload()))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"model_call_count":1}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		state, err := run.ParseExecutionState(executionStateKind, payload)
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
				captured, err := run.ParseExecutionState(executionStateKind, payload)
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
	context := &Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}}
	for _, test := range []struct {
		name  string
		state executionState
		valid bool
	}{
		{name: "completion marker", state: executionState{Completed: true}, valid: true},
		{name: "repeated model calls", state: executionState{ModelCallCount: 1, Completed: true}},
		{name: "repeated context", state: executionState{WorkingContext: context, Completed: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, err := test.state.snapshot()
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

// fuzzWorkerDefinition supplies the Delegate contract the fuzz state targets;
// restoration never runs it.
type fuzzWorkerDefinition struct{ descriptor run.Descriptor }

func (f fuzzWorkerDefinition) Descriptor() run.Descriptor { return f.descriptor }
func (fuzzWorkerDefinition) ChildDeployments() []run.Program[run.Payload, run.Payload] {
	return nil
}
func (fuzzWorkerDefinition) Start(run.Payload) (run.Execution, error) {
	return nil, errors.New("fuzz worker never starts")
}
func (fuzzWorkerDefinition) Restore(context.Context, run.ExecutionState) (run.Execution, error) {
	return nil, errors.New("fuzz worker never restores")
}

func fuzzInteractionDefinition(f testing.TB) *Definition {
	f.Helper()
	inputSchema, err := jsonschema.For[fuzzDelegateInput]()
	if err != nil {
		f.Fatal(err)
	}
	outputSchema, err := jsonschema.For[fuzzDelegateOutput]()
	if err != nil {
		f.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: "interaction.fuzz_worker", Description: "Provide a deterministic fuzz worker contract.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		f.Fatal(err)
	}
	workerDeployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: fuzzWorkerDefinition{descriptor: descriptor}, Dispatcher: fuzzDispatcher{},
		ImplementationDigest: run.ComputeDigest([]byte("interaction-fuzz-worker-implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte("interaction-fuzz-worker-configuration")),
	})
	if err != nil {
		f.Fatal(err)
	}
	budget := run.Budget{Steps: run.NewQuota(10), Effects: run.NewQuota(10), Signals: run.NewQuota(10)}
	delegate := Delegate{
		definition: toolcontract.Definition{
			Name: "delegate_fuzz", Description: "Delegate one fuzz task to the exact worker.",
			InputSchema: inputSchema.JSON(),
		},
		deployment: workerDeployment,
		budget:     budget, capabilities: run.CapabilitySet{},
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "interaction.fuzz", Description: "Exercise strict Interaction state restoration.",
		MaxModelCalls: run.NewQuota(4), Delegates: []Delegate{delegate},
	})
	if err != nil {
		f.Fatal(err)
	}
	return definition
}

func fuzzInteractionStates(f testing.TB, definition *Definition) []run.ExecutionState {
	f.Helper()
	request := &Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("fuzz"))}}
	call := chat.ToolCall{ID: "call_fuzz", Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
	message := chat.NewAssistantMessage(chat.NewToolCallPart(call))
	response := &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}
	processID, _ := run.ParseProcessID("process:fuzz-child")
	waitID, _ := run.ParseWaitID("wait:fuzz-child")
	steerSignalID, _ := run.ParseSignalID("signal:fuzz-steer")
	artifactOutput, _ := run.EncodePayload(fuzzDelegateOutput{Result: "settled"})
	states := []executionState{
		{
			WorkingContext: request.clone(), ModelCallCount: 1,
			ToolRound: &toolCallRound{Response: response.Clone(), Results: []toolCallResult{{
				Disposition: ResultRejected, Output: toolcontract.NewTextOutput("worker unavailable"),
			}}},
		},
		{
			WorkingContext: request.clone(), ModelCallCount: 1,
			ToolRound: &toolCallRound{Response: response.Clone(),
				ChildBatch: &childCallBatch{Kind: childCallsDelegate, Invocations: []*childInvocationState{{}}}},
		},
		{
			WorkingContext: request.clone(), ModelCallCount: 1,
			PendingSteer: &steerBatch{
				Messages:  []chat.Message{chat.NewUserMessage(chat.NewTextPart("fuzz steer"))},
				SignalIDs: []run.SignalID{steerSignalID},
			},
			ToolRound: &toolCallRound{Response: response.Clone(), ChildBatch: &childCallBatch{Kind: childCallsDelegate, WaitID: &waitID, Invocations: []*childInvocationState{{
				ProcessID: &processID,
			}}}},
		},
		{
			WorkingContext: request.clone(), ModelCallCount: 2,
			ArtifactRecords: []artifactRecord{{
				ModelCallSequence: 1, ToolCallID: "call_settled",
				DelegateName: "delegate_fuzz", Output: artifactOutput,
			}},
		},
	}
	encoded := make([]run.ExecutionState, 0, len(states))
	for _, state := range states {
		if err := state.validate(f.Context(), definition); err != nil {
			f.Fatal(err)
		}
		payload, err := jsonv2.Marshal(state)
		if err != nil {
			f.Fatal(err)
		}
		envelope, err := run.ParseExecutionState(executionStateKind, payload)
		if err != nil {
			f.Fatal(err)
		}
		encoded = append(encoded, envelope)
	}
	return encoded
}
