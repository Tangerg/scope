package runtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

type definitionConformanceInput struct {
	Value uint64 `json:"value"`
}

type definitionConformanceDefinition struct {
	descriptor run.Descriptor
	counter    atomic.Uint64
	shared     *uint64
	lossy      bool
}

func newDefinitionConformanceFixture(t *testing.T) *definitionConformanceDefinition {
	t.Helper()
	schema, err := jsonschema.For[definitionConformanceInput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
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

func (d *definitionConformanceDefinition) Descriptor() run.Descriptor {
	return d.descriptor
}

func (*definitionConformanceDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (d *definitionConformanceDefinition) Start(input run.Payload) (run.Execution, error) {
	value, err := input.Decode[definitionConformanceInput]()
	if err != nil {
		return nil, err
	}
	if d.shared != nil {
		return &definitionConformanceExecution{definition: d, shared: d.shared}, nil
	}
	return &definitionConformanceExecution{definition: d, value: value.Value}, nil
}

func (d *definitionConformanceDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := state.Decode[definitionConformanceInput]("agenttest.definition_conformance")
	if err != nil {
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

func (d *definitionConformanceExecution) Step(_ context.Context, _ run.Turn) (run.Transition, error) {
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
	return run.Continue(0)
}

func (d *definitionConformanceExecution) Snapshot() (run.ExecutionState, error) {
	value := d.value
	if d.shared != nil {
		value = *d.shared
	}
	return run.EncodeExecutionState("agenttest.definition_conformance", definitionConformanceInput{Value: value})
}

func TestRunDefinitionConformanceAcceptsIsolatedDeterministicDefinition(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	input, err := run.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, stepErr := execution.Step(context.Background(), definitionTurn(1)); stepErr != nil {
		t.Fatal(stepErr)
	}
	restoredState, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	RunDefinitionConformance(t, DefinitionConformanceConfig{InitialTurn: definitionTurn(1),
		Definition:     definition,
		Input:          input,
		FollowingTurns: []run.Turn{definitionTurn(2), definitionTurn(3)},
		RestoredCases: []ExecutionConformanceCase{{
			Name: "after one Step", State: restoredState, Turn: definitionTurn(2),
		}},
	})
}

func TestDefinitionConformanceDetectsHiddenMutableInput(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	definition.counter.Store(1)
	input, err := run.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyFreshExecutions(t.Context(), DefinitionConformanceConfig{InitialTurn: definitionTurn(1),
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
	input, err := run.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyFreshExecutions(t.Context(), DefinitionConformanceConfig{InitialTurn: definitionTurn(1),
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
	input, err := run.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyFreshExecutions(t.Context(), DefinitionConformanceConfig{InitialTurn: definitionTurn(1), Definition: definition, Input: input})
	if !errors.Is(err, errConformanceValuesDiffer) {
		t.Fatalf("lossy restore = %v", err)
	}
}

func TestDefinitionConformanceValidatesEveryConfiguredTurn(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	input, err := run.EncodePayload(definitionConformanceInput{Value: 7})
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
	invalid := definitionTurn(1)
	invalid.Messages = run.Inbox{run.Signal{}}
	for _, test := range []struct {
		name   string
		mutate func(*DefinitionConformanceConfig)
		want   string
	}{
		{"initial", func(c *DefinitionConformanceConfig) { c.InitialTurn = invalid }, "fresh Turn"},
		{"following", func(c *DefinitionConformanceConfig) { c.FollowingTurns = []run.Turn{definitionTurn(2), invalid} }, "fresh Turn"},
		{"restored", func(c *DefinitionConformanceConfig) { c.RestoredCases[0].Turn = invalid }, "restored case"},
		{"restored following", func(c *DefinitionConformanceConfig) { c.RestoredCases[0].FollowingTurns = []run.Turn{invalid} }, "restored case"},
		{"rejected name", func(c *DefinitionConformanceConfig) { c.RejectedCases[0].Name = " padded" }, "invalid name"},
		{"rejected duplicate", func(c *DefinitionConformanceConfig) { c.RejectedCases = append(c.RejectedCases, c.RejectedCases[0]) }, "duplicated"},
		{"rejected state", func(c *DefinitionConformanceConfig) { c.RejectedCases[0].State = run.ExecutionState{} }, "invalid state"},
		{"rejected turn", func(c *DefinitionConformanceConfig) { c.RejectedCases[0].Turn = invalid }, "rejected case"},
		{"rejected kind", func(c *DefinitionConformanceConfig) { c.RejectedCases[0].FailureKind = run.FailureKindInvalid }, "exact Failure kind and code"},
		{"rejected code", func(c *DefinitionConformanceConfig) { c.RejectedCases[0].FailureCode = "Rejected" }, "exact Failure kind and code"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefinitionConformanceConfig{
				Definition: definition, Input: input, InitialTurn: definitionTurn(1),
				RestoredCases: []ExecutionConformanceCase{{Name: "sample", State: state, Turn: definitionTurn(1)}},
				RejectedCases: []RejectedStepConformanceCase{{Name: "rejected", State: state, Turn: definitionTurn(1), FailureKind: run.FailureKindContract, FailureCode: "agenttest.rejected"}},
			}
			test.mutate(&config)
			if err := validateDefinitionConformanceConfig(config); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid config error = %v, want %q", err, test.want)
			}
		})
	}
}

func definitionTurn(step uint64) run.Turn {
	return run.Turn{Process: (run.StartID{Scope: "runtest", Key: "definition"}).Root(), Step: step}
}

type conformanceContextExecution struct {
	seen context.Context
}

func (c *conformanceContextExecution) Step(ctx context.Context, _ run.Turn) (run.Transition, error) {
	c.seen = ctx
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	return run.Continue(0)
}

func (c *conformanceContextExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("test.context", 0)
}

func TestConformanceStepPreservesCallerCancellationAndClosesItsScope(t *testing.T) {
	execution := &conformanceContextExecution{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := callStep(ctx, execution, definitionTurn(1)); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(execution.seen.Err(), context.Canceled) || ctx.Err() != nil {
		t.Fatal("Step must close its own context without canceling the caller")
	}
	cancel()
	if _, err := callStep(ctx, execution, definitionTurn(1)); !errors.Is(err, context.Canceled) {
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

type rejectingDefinition struct {
	descriptor run.Descriptor
	err        error
}

func newRejectingDefinition(t *testing.T, err error) *rejectingDefinition {
	t.Helper()
	schema, schemaErr := jsonschema.For[definitionConformanceInput]()
	if schemaErr != nil {
		t.Fatal(schemaErr)
	}
	descriptor, descriptorErr := run.NewDescriptor(run.DescriptorConfig{
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

func (r *rejectingDefinition) Descriptor() run.Descriptor { return r.descriptor }

func (*rejectingDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (r *rejectingDefinition) Start(run.Payload) (run.Execution, error) {
	return &rejectingExecution{err: r.err}, nil
}

func (r *rejectingDefinition) Restore(context.Context, run.ExecutionState) (run.Execution, error) {
	return &rejectingExecution{err: r.err}, nil
}

type rejectingExecution struct{ err error }

func (r *rejectingExecution) Step(context.Context, run.Turn) (run.Transition, error) {
	if r.err == nil {
		return run.Continue(0)
	}
	return run.Transition{}, r.err
}

func (r *rejectingExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("agenttest.rejecting", definitionConformanceInput{})
}

func TestVerifyRejectedStepRequiresAStableClassification(t *testing.T) {
	declared := run.NewClassifiedError(run.FailureKindContract, "agenttest.rejected", "runtest: rejected")
	state, err := (&rejectingExecution{}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	sample := RejectedStepConformanceCase{Turn: definitionTurn(1),
		Name: "sample", State: state,
		FailureKind: run.FailureKindContract, FailureCode: "agenttest.rejected",
	}
	if verifyErr := verifyRejectedStep(t.Context(),
		newRejectingDefinition(t, fmt.Errorf("step: %w", declared)), sample,
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
		{
			"wrong classification",
			run.NewClassifiedError(run.FailureKindExecution, "agenttest.other", "runtest: other"),
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

// terminalDefinition completes on its first Step and owes no state afterwards,
// exactly as the Runtime treats a terminated Execution.
type terminalDefinition struct{ descriptor run.Descriptor }

func (t terminalDefinition) Descriptor() run.Descriptor                           { return t.descriptor }
func (terminalDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }
func (terminalDefinition) Start(run.Payload) (run.Execution, error)               { return &terminalExecution{}, nil }

func (terminalDefinition) Restore(context.Context, run.ExecutionState) (run.Execution, error) {
	return &terminalExecution{}, nil
}

type terminalExecution struct{ done bool }

func (t *terminalExecution) Step(context.Context, run.Turn) (run.Transition, error) {
	t.done = true
	output, err := run.EncodePayload(definitionConformanceInput{Value: 1})
	if err != nil {
		return run.Transition{}, err
	}
	return run.Complete(0, output)
}

func (t *terminalExecution) Snapshot() (run.ExecutionState, error) {
	if t.done {
		return run.ExecutionState{}, errors.New("a terminated Execution has no state to snapshot")
	}
	return run.EncodeExecutionState("agenttest.terminal", struct{}{})
}

func TestDefinitionConformanceEndsAtTheTerminalTransition(t *testing.T) {
	definition := terminalDefinition{descriptor: newDefinitionConformanceFixture(t).descriptor}
	input, err := run.EncodePayload(definitionConformanceInput{Value: 1})
	if err != nil {
		t.Fatal(err)
	}
	config := DefinitionConformanceConfig{Definition: definition, Input: input, InitialTurn: definitionTurn(1)}
	if err := verifyFreshExecutions(t.Context(), config); err != nil {
		t.Fatalf("terminal Step demanded post-termination state: %v", err)
	}
	config.FollowingTurns = []run.Turn{definitionTurn(2)}
	if err := verifyFreshExecutions(t.Context(), config); err == nil || !strings.Contains(err.Error(), "follows a terminal Transition") {
		t.Fatalf("Turn after termination = %v", err)
	}
}
