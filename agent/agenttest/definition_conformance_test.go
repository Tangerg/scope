package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

type definitionConformanceInput struct {
	Value uint64 `json:"value"`
}

type definitionConformanceDefinition struct {
	descriptor agent.Descriptor
	counter    atomic.Uint64
	shared     *uint64
	lossy      bool
}

func newDefinitionConformanceFixture(t *testing.T) *definitionConformanceDefinition {
	t.Helper()
	schema, err := agent.SchemaFor[definitionConformanceInput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name:         "agenttest.definition_conformance",
		Description:  "Exercise Definition and Execution conformance checks.",
		InputSchema:  schema,
		OutputSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &definitionConformanceDefinition{descriptor: descriptor}
}

func (d *definitionConformanceDefinition) Descriptor() agent.Descriptor {
	return d.descriptor
}

func (d *definitionConformanceDefinition) Start(input agent.Input) (agent.Execution, error) {
	value, err := input.Decode[definitionConformanceInput]()
	if err != nil {
		return nil, err
	}
	if d.shared != nil {
		return &definitionConformanceExecution{definition: d, shared: d.shared}, nil
	}
	return &definitionConformanceExecution{definition: d, value: value.Value}, nil
}

func (d *definitionConformanceDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	var value definitionConformanceInput
	if err := json.Unmarshal(state.Payload(), &value); err != nil {
		return nil, err
	}
	return &definitionConformanceExecution{definition: d, value: value.Value, restored: d.lossy}, nil
}

type definitionConformanceExecution struct {
	definition *definitionConformanceDefinition
	value      uint64
	restored   bool
	shared     *uint64
}

func (d *definitionConformanceExecution) Step(_ context.Context, _ []agent.Signal) (agent.Transition, error) {
	if d.restored {
		d.value += 10
	}
	if d.shared != nil {
		*d.shared++
	} else if d.definition.counter.Load() != 0 {
		d.value = d.definition.counter.Add(1)
	} else {
		d.value++
	}
	return agent.Continue(0)
}

func (d *definitionConformanceExecution) Snapshot() (agent.ExecutionState, error) {
	value := d.value
	if d.shared != nil {
		value = *d.shared
	}
	payload, err := json.Marshal(definitionConformanceInput{Value: value})
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("agenttest.definition_conformance", payload)
}

func TestRunDefinitionConformanceAcceptsIsolatedDeterministicDefinition(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	input, err := agent.EncodeInput(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, stepErr := execution.Step(context.Background(), nil); stepErr != nil {
		t.Fatal(stepErr)
	}
	restoredState, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	RunDefinitionConformance(t, DefinitionConformanceConfig{
		Definition:       definition,
		Input:            input,
		FollowingSignals: [][]agent.Signal{nil, nil},
		RestoredCases: []ExecutionConformanceCase{{
			Name: "after one Step", State: restoredState,
		}},
	})
}

func TestDefinitionConformanceDetectsHiddenMutableInput(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	definition.counter.Store(1)
	input, err := agent.EncodeInput(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyFreshExecutions(DefinitionConformanceConfig{
		Definition: definition,
		Input:      input,
	})
	if !errors.Is(err, errConformanceValuesDiffer) {
		t.Fatalf("hidden mutable input error = %v", err)
	}
}

func TestDefinitionConformanceDetectsSharedExecutionState(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	shared := uint64(7)
	definition.shared = &shared
	input, err := agent.EncodeInput(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyFreshExecutions(DefinitionConformanceConfig{
		Definition: definition,
		Input:      input,
	})
	if !errors.Is(err, errConformanceExecutionsShareState) {
		t.Fatalf("shared mutable state error = %v", err)
	}
}

func TestDefinitionConformanceRejectsLossyBehaviorRestore(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	definition.lossy = true
	input, err := agent.EncodeInput(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyFreshExecutions(DefinitionConformanceConfig{Definition: definition, Input: input})
	if !errors.Is(err, errConformanceValuesDiffer) {
		t.Fatalf("lossy restore = %v", err)
	}
}
