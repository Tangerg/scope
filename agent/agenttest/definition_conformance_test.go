package agenttest

import (
	"context"
	jsonv2 "encoding/json/v2"
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
	if err := jsonv2.Unmarshal(state.Payload(), &value); err != nil {
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
	payload, err := jsonv2.Marshal(definitionConformanceInput{Value: value})
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.ParseExecutionState("agenttest.definition_conformance", payload)
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
		{"rejected name", func(config *DefinitionConformanceConfig) {
			config.RejectedCases[0].Name = " padded"
		}, "rejected case 0 has an invalid name"},
		{"rejected duplicate", func(config *DefinitionConformanceConfig) {
			config.RejectedCases = append(config.RejectedCases, config.RejectedCases[0])
		}, "rejected case name \"rejected\" is duplicated"},
		{"rejected state", func(config *DefinitionConformanceConfig) {
			config.RejectedCases[0].State = agent.ExecutionState{}
		}, "rejected case \"rejected\" has an invalid state"},
		{"rejected signals", func(config *DefinitionConformanceConfig) {
			config.RejectedCases[0].Signals = []agent.Signal{{}}
		}, "rejected case \"rejected\" Signals: batch 0 signal 0 is invalid"},
		{"rejected kind", func(config *DefinitionConformanceConfig) {
			config.RejectedCases[0].FailureKind = agent.FailureKindInvalid
		}, "rejected case \"rejected\" must name the exact Failure kind and code"},
		{"rejected code", func(config *DefinitionConformanceConfig) {
			config.RejectedCases[0].FailureCode = "Rejected"
		}, "rejected case \"rejected\" must name the exact Failure kind and code"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefinitionConformanceConfig{
				Definition: definition, Input: input,
				RestoredCases: []ExecutionConformanceCase{{Name: "sample", State: state}},
				RejectedCases: []RejectedStepConformanceCase{{
					Name: "rejected", State: state,
					FailureKind: agent.FailureKindContract, FailureCode: "agenttest.rejected",
				}},
			}
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

func TestEquivalentStateIgnoresObjectMemberOrder(t *testing.T) {
	left := map[string]any{"first": 1, "second": map[string]any{"a": true, "b": false}}
	right := map[string]any{"second": map[string]any{"b": false, "a": true}, "first": 1}
	for range 32 {
		if err := requireEquivalent("state", left, right); err != nil {
			t.Fatal(err)
		}
	}
	right["first"] = 2
	if err := requireEquivalent("state", left, right); !errors.Is(err, errConformanceValuesDiffer) {
		t.Fatalf("changed state = %v, want conformance mismatch", err)
	}
}

// rejectingDefinition returns one configured Step outcome so the rejection
// check can be exercised against every shape a Definition might produce.
type rejectingDefinition struct {
	descriptor agent.Descriptor
	err        error
}

func newRejectingDefinition(t *testing.T, err error) *rejectingDefinition {
	t.Helper()
	schema, schemaErr := agent.SchemaFor[definitionConformanceInput]()
	if schemaErr != nil {
		t.Fatal(schemaErr)
	}
	descriptor, descriptorErr := agent.NewDescriptor(agent.DescriptorConfig{
		Name:         "agenttest.rejecting",
		Description:  "Exercise rejected-Step conformance checks.",
		InputSchema:  schema,
		OutputSchema: schema,
	})
	if descriptorErr != nil {
		t.Fatal(descriptorErr)
	}
	return &rejectingDefinition{descriptor: descriptor, err: err}
}

func (r *rejectingDefinition) Descriptor() agent.Descriptor { return r.descriptor }

func (r *rejectingDefinition) Start(agent.Payload) (agent.Execution, error) {
	return &rejectingExecution{err: r.err}, nil
}

func (r *rejectingDefinition) Restore(context.Context, agent.ExecutionState) (agent.Execution, error) {
	return &rejectingExecution{err: r.err}, nil
}

type rejectingExecution struct{ err error }

func (r *rejectingExecution) Step(context.Context, []agent.Signal) (agent.Transition, error) {
	if r.err == nil {
		return agent.Continue(0)
	}
	return agent.Transition{}, r.err
}

func (r *rejectingExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := jsonv2.Marshal(definitionConformanceInput{})
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.ParseExecutionState("agenttest.rejecting", payload)
}

func TestVerifyRejectedStepRequiresAStableClassification(t *testing.T) {
	declared, err := agent.NewFailure(agent.FailureKindContract, "agenttest.rejected", "rejected")
	if err != nil {
		t.Fatal(err)
	}
	state, err := (&rejectingExecution{}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	sample := RejectedStepConformanceCase{
		Name: "sample", State: state,
		FailureKind: agent.FailureKindContract, FailureCode: "agenttest.rejected",
	}
	if verifyErr := verifyRejectedStep(t.Context(),
		newRejectingDefinition(t, &agent.StepError{Failure: declared}), sample,
	); verifyErr != nil {
		t.Fatalf("a correctly classified rejection failed: %v", verifyErr)
	}
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"accepted", nil, "instead of a classified Failure"},
		{"unclassified", errors.New("plain failure"), "unclassified error"},
		{"invalid failure", &agent.StepError{}, "invalid Failure"},
		{
			"wrong classification",
			&agent.StepError{Failure: mustFailure(t, agent.FailureKindExecution, "agenttest.other")},
			"want contract/agenttest.rejected",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifyErr := verifyRejectedStep(t.Context(), newRejectingDefinition(t, test.err), sample)
			if verifyErr == nil || !strings.Contains(verifyErr.Error(), test.want) {
				t.Fatalf("rejection check error = %v, want %q", verifyErr, test.want)
			}
		})
	}
}

func mustFailure(t *testing.T, kind agent.FailureKind, code string) agent.Failure {
	t.Helper()
	failure, err := agent.NewFailure(kind, code, "diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	return failure
}
