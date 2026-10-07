package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

type stateFixture struct {
	Value int `json:"value"`
}

func TestRestoreRejectsUnknownAndContradictoryState(t *testing.T) {
	definition := stateTestDefinition(t)
	for name, payload := range map[string]json.RawMessage{
		"missing stage index":      json.RawMessage(`{"current_value":{"value":1}}`),
		"null stage index":         json.RawMessage(`{"stage_index":null,"current_value":{"value":1}}`),
		"unknown field":            json.RawMessage(`{"stage_index":0,"current_value":{"value":1},"unknown":true}`),
		"child in transform":       json.RawMessage(`{"stage_index":0,"current_value":{"value":1},"child":{}}`),
		"Loop cursor in Transform": json.RawMessage(`{"stage_index":0,"current_value":{"value":1},"loop_iteration":1}`),
	} {
		t.Run(name, func(t *testing.T) {
			state, err := agent.ParseExecutionState(executionStateKind, payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := definition.Restore(t.Context(), state); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("Restore error = %v", err)
			}
		})
	}
	validPayload := json.RawMessage(`{"stage_index":0,"current_value":{"value":1}}`)
	state, err := agent.ParseExecutionState("other", validPayload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Restore(t.Context(), state); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("Restore envelope error = %v", err)
	}
}

func TestExecutionRejectsMissingProtocolSignals(t *testing.T) {
	callDefinition, fanoutDefinition := protocolTestDefinitions(t)
	for name, test := range map[string]struct {
		definition *Definition
		payload    string
	}{
		"child start": {
			definition: callDefinition,
			payload:    `{"stage_index":0,"current_value":{"value":1},"child":{}}`,
		},
		"child wait opening": {
			definition: callDefinition,
			payload:    `{"stage_index":0,"current_value":{"value":1},"child":{"process_id":"child"}}`,
		},
		"child completion": {
			definition: callDefinition,
			payload:    `{"stage_index":0,"current_value":{"value":1},"child":{"process_id":"child","wait_id":"wait"}}`,
		},
		"fan-out starts": {
			definition: fanoutDefinition,
			payload:    `{"stage_index":0,"current_value":{"value":1},"active_fanout_window":[{}]}`,
		},
		"fan-out wait opening": {
			definition: fanoutDefinition,
			payload:    `{"stage_index":0,"current_value":{"value":1},"active_fanout_window":[{"child_process_id":"child"}]}`,
		},
		"fan-out completion": {
			definition: fanoutDefinition,
			payload:    `{"stage_index":0,"current_value":{"value":1},"fanout_wait_id":"wait","active_fanout_window":[{"child_process_id":"child"}]}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			state, err := agent.ParseExecutionState(executionStateKind, json.RawMessage(test.payload))
			if err != nil {
				t.Fatal(err)
			}
			execution, err := test.definition.Restore(t.Context(), state)
			if err != nil {
				t.Fatal(err)
			}
			_, stepErr := execution.Step(context.Background(), nil)
			if !errors.Is(stepErr, ErrInvalidProtocol) {
				t.Fatalf("Step error = %v", stepErr)
			}
			failure, ok := agent.StepFailure(stepErr)
			if !ok || failure.Kind() != agent.FailureKindContract ||
				failure.Code() != "workflow.protocol.invalid" {
				t.Fatalf("rejection classification = %v", stepErr)
			}
		})
	}
}

func TestRestorePreservesExplicitNullBusinessValue(t *testing.T) {
	stage, err := Transform("identity", func(_ context.Context, value any) (any, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "test.workflow.null", Description: "Preserve nullable business values.", Stages: []Stage{stage},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := agent.ParseExecutionState(executionStateKind, []byte(`{"stage_index":0,"current_value":null}`))
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Restore(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	transition, err := execution.Step(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	output, completed := transition.Output()
	if !completed || string(output.JSON()) != "null" {
		t.Fatalf("nullable value lost at completion: %+v", transition)
	}
}

func TestRestoreRejectsContradictorySingleChildProgress(t *testing.T) {
	definition, _ := protocolTestDefinitions(t)
	for _, payload := range []string{
		`{"stage_index":0,"current_value":{"value":1},"child":{"wait_id":"wait"}}`,
		`{"stage_index":0,"current_value":{"value":1},"child":{},"fanout_wait_id":"wait"}`,
	} {
		state, err := agent.ParseExecutionState(executionStateKind, json.RawMessage(payload))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := definition.Restore(t.Context(), state); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("Restore(%s) error=%v", payload, err)
		}
	}
}

func FuzzWorkflowExecutionStateRestore(f *testing.F) {
	definition := stateTestDefinition(f)
	f.Add([]byte(`{"stage_index":0,"current_value":{"value":1}}`))
	f.Add([]byte(`{"stage_index":1,"current_value":{"value":1}}`))
	f.Add([]byte(`{"stage_index":0,"current_value":{"value":1}}`))
	f.Add([]byte(`{"stage_index":0,"current_value":{"value":1},"unknown":true}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		state, err := agent.ParseExecutionState(executionStateKind, payload)
		if err != nil {
			return
		}
		execution, err := definition.Restore(t.Context(), state)
		if err != nil {
			return
		}
		restored, err := execution.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := definition.Restore(t.Context(), restored); err != nil {
			t.Fatalf("accepted state is not restorable: %v", err)
		}
	})
}

func stateTestDefinition(t testing.TB) *Definition {
	t.Helper()
	stage, err := Transform("identity", func(_ context.Context, input stateFixture) (stateFixture, error) { return input, nil })
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewDefinition(DefinitionConfig{
		Name: "test.workflow.state", Description: "Validate Workflow state restoration.",
		Stages: []Stage{stage},
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func protocolTestDefinitions(t testing.TB) (*Definition, *Definition) {
	t.Helper()
	childDefinition := stateTestDefinition(t)
	child, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           childDefinition,
		ImplementationDigest: agent.ComputeDigest([]byte("workflow-state-protocol-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("workflow-state-protocol-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	budget := agent.Budget{Steps: agent.NewQuota(8), Effects: agent.NewQuota(8), Signals: agent.NewQuota(8)}
	call, err := Call(CallConfig{ID: "child", Deployment: child, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	callDefinition, err := NewDefinition(DefinitionConfig{
		Name: "test.workflow.call_protocol", Description: "Validate Call protocol failures.",
		Stages: []Stage{call},
	})
	if err != nil {
		t.Fatal(err)
	}
	fanout, err := Fork(ForkConfig[stateFixture, stateFixture, stateFixture]{
		ID:         "fanout",
		Branches:   []ForkBranch{{ID: "only", Deployment: child, Budget: budget}},
		WindowSize: 1,
		Reduce: func(_ context.Context, _ stateFixture, values []stateFixture) (stateFixture, error) {
			return values[0], nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fanoutDefinition, err := NewDefinition(DefinitionConfig{
		Name: "test.workflow.fanout_protocol", Description: "Validate fan-out protocol failures.",
		Stages: []Stage{fanout},
	})
	if err != nil {
		t.Fatal(err)
	}
	return callDefinition, fanoutDefinition
}

func TestCompletedStateLeavesOutputToTheEngine(t *testing.T) {
	for payload, valid := range map[string]bool{
		`{"stage_index":1}`: true,
		`{"stage_index":1,"current_value":{"value":"done"}}`: false,
	} {
		state, err := agent.ParseExecutionState(executionStateKind, json.RawMessage(payload))
		if err != nil {
			t.Fatal(err)
		}
		_, err = stateTestDefinition(t).Restore(t.Context(), state)
		if valid && err != nil || !valid && !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("Restore(%s) error = %v, want valid=%t", payload, err, valid)
		}
	}
}

func TestRestoreIdentifiesContradictoryFanoutProgress(t *testing.T) {
	_, definition := protocolTestDefinitions(t)
	for _, test := range []struct {
		payload string
		context string
	}{
		{
			payload: `{"stage_index":0,"current_value":{"value":1},"fanout_wait_id":"wait"}`,
			context: "active fan-out window does not match source boundaries",
		},
		{
			payload: `{"stage_index":0,"current_value":{"value":1},"fanout_wait_id":"wait","active_fanout_window":[{}]}`,
			context: "fan-out wait requires started children",
		},
		{
			payload: `{"stage_index":0,"current_value":{"value":1},"active_fanout_window":[` +
				`{"failure":{"kind":"external","code":"child.refused","message":"refused"}}]}`,
			context: "fan-out wait requires started children",
		},
		{
			payload: `{"stage_index":0,"current_value":{"value":1},"fanout_wait_id":"wait","active_fanout_window":[` +
				`{"child_process_id":"process:child","failure":{"kind":"external","code":"child.failed","message":"failed"}}]}`,
			context: "a started fan-out child repeats a Failure the Engine owns",
		},
	} {
		state, err := agent.ParseExecutionState(executionStateKind, json.RawMessage(test.payload))
		if err != nil {
			t.Fatal(err)
		}
		_, err = definition.Restore(t.Context(), state)
		if !errors.Is(err, ErrInvalidExecutionState) || !strings.Contains(err.Error(), test.context) {
			t.Fatalf("Restore error = %v, want invalid state with %q", err, test.context)
		}
	}
}
