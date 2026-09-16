package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func (d *definitionConformanceDefinition) Start(input agent.Payload) (agent.Execution, error) {
	value, err := input.Decode[definitionConformanceInput]()
	if err != nil {
		return nil, err
	}
	if d.shared != nil {
		return &definitionConformanceExecution{definition: d, shared: d.shared}, nil
	}
	return &definitionConformanceExecution{definition: d, value: value.Value}, nil
}

func (d *definitionConformanceDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
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
	input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
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
	input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyFreshExecutions(t.Context(), DefinitionConformanceConfig{
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
	input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyFreshExecutions(t.Context(), DefinitionConformanceConfig{
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
	input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyFreshExecutions(t.Context(), DefinitionConformanceConfig{Definition: definition, Input: input})
	if !errors.Is(err, errConformanceValuesDiffer) {
		t.Fatalf("lossy restore = %v", err)
	}
}

func TestDefinitionConformanceValidatesEveryConfiguredSignalBatch(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	state, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*DefinitionConformanceConfig)
		want   string
	}{
		{"initial", func(config *DefinitionConformanceConfig) { config.InitialSignals = []agent.Signal{{}} }, "fresh Signals: batch 0 signal 0 is invalid"},
		{"following", func(config *DefinitionConformanceConfig) { config.FollowingSignals = [][]agent.Signal{nil, {{}}} }, "fresh Signals: batch 2 signal 0 is invalid"},
		{"restored", func(config *DefinitionConformanceConfig) { config.RestoredCases[0].Signals = []agent.Signal{{}} }, "restored case \"sample\" Signals: batch 0 signal 0 is invalid"},
		{"restored following", func(config *DefinitionConformanceConfig) {
			config.RestoredCases[0].FollowingSignals = [][]agent.Signal{nil, {{}}}
		}, "restored case \"sample\" Signals: batch 2 signal 0 is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefinitionConformanceConfig{Definition: definition, Input: input, RestoredCases: []ExecutionConformanceCase{{Name: "sample", State: state}}}
			test.mutate(&config)
			if err := validateDefinitionConformanceConfig(config); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid config error = %v, want %q", err, test.want)
			}
		})
	}
}

type conformanceContextExecution struct {
	seen context.Context
}

func (c *conformanceContextExecution) Step(ctx context.Context, _ []agent.Signal) (agent.Transition, error) {
	c.seen = ctx
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	return agent.Continue(0)
}

func (c *conformanceContextExecution) Snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState("test.context", 0)
}

func TestConformanceStepPreservesCallerCancellationAndClosesItsScope(t *testing.T) {
	execution := &conformanceContextExecution{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := callStep(ctx, execution, nil); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(execution.seen.Err(), context.Canceled) || ctx.Err() != nil {
		t.Fatal("Step must close its own context without canceling the caller")
	}
	cancel()
	if _, err := callStep(ctx, execution, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation did not reach Step: %v", err)
	}
}
