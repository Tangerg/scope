package agenttest

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent"
)

type definitionProbe struct {
	describe func() agent.Descriptor
	start    func(agent.Payload) (agent.Execution, error)
	restore  func(context.Context, agent.ExecutionState) (agent.Execution, error)
}

func (d *definitionProbe) Descriptor() agent.Descriptor                       { return d.describe() }
func (*definitionProbe) ChildDeployments() []agent.Deployment                 { return nil }
func (d *definitionProbe) Start(input agent.Payload) (agent.Execution, error) { return d.start(input) }
func (d *definitionProbe) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	return d.restore(ctx, state)
}

func (d *definitionProbe) decorateStart(decorate func(agent.Execution) agent.Execution) {
	start := d.start
	d.start = func(input agent.Payload) (agent.Execution, error) {
		execution, err := start(input)
		if err != nil {
			return nil, err
		}
		return decorate(execution), nil
	}
}

type executionProbe struct {
	step     func(context.Context, []agent.Signal) (agent.Transition, error)
	snapshot func() (agent.ExecutionState, error)
}

func (e *executionProbe) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	return e.step(ctx, signals)
}
func (e *executionProbe) Snapshot() (agent.ExecutionState, error) { return e.snapshot() }

func TestDefinitionConformanceRejectsBrokenLifecycleContracts(t *testing.T) {
	cause := errors.New("fixture callback refused")
	for _, test := range []struct {
		name      string
		configure func(*definitionProbe)
		want      string
	}{
		{"invalid descriptor", func(probe *definitionProbe) {
			probe.describe = func() agent.Descriptor { return agent.Descriptor{} }
		}, "invalid Descriptor"},
		{"start failure", func(probe *definitionProbe) {
			probe.start = func(agent.Payload) (agent.Execution, error) { return nil, cause }
		}, "Definition.Start: fixture callback refused"},
		{"nil start", func(probe *definitionProbe) {
			probe.start = func(agent.Payload) (agent.Execution, error) { return (*executionProbe)(nil), nil }
		}, "Start returned a nil Execution"},
		{"snapshot failure", func(probe *definitionProbe) {
			probe.decorateStart(func(execution agent.Execution) agent.Execution {
				return &executionProbe{step: execution.Step, snapshot: func() (agent.ExecutionState, error) { return agent.ExecutionState{}, cause }}
			})
		}, "Execution.Snapshot: fixture callback refused"},
		{"invalid snapshot", func(probe *definitionProbe) {
			probe.decorateStart(func(execution agent.Execution) agent.Execution {
				return &executionProbe{step: execution.Step, snapshot: func() (agent.ExecutionState, error) { return agent.ExecutionState{}, nil }}
			})
		}, "Snapshot returned an invalid state"},
		{"restore failure", func(probe *definitionProbe) {
			probe.restore = func(context.Context, agent.ExecutionState) (agent.Execution, error) { return nil, cause }
		}, "Definition.Restore: fixture callback refused"},
		{"nil restore", func(probe *definitionProbe) {
			probe.restore = func(context.Context, agent.ExecutionState) (agent.Execution, error) {
				return (*executionProbe)(nil), nil
			}
		}, "Restore returned a nil Execution"},
		{"invalid restored snapshot", func(probe *definitionProbe) {
			restore := probe.restore
			probe.restore = func(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
				execution, err := restore(ctx, state)
				if err != nil {
					return nil, err
				}
				return &executionProbe{step: execution.Step, snapshot: func() (agent.ExecutionState, error) { return agent.ExecutionState{}, nil }}, nil
			}
		}, "Snapshot returned an invalid state"},
		{"invalid transition", func(probe *definitionProbe) {
			probe.decorateStart(func(execution agent.Execution) agent.Execution {
				return &executionProbe{snapshot: execution.Snapshot, step: func(context.Context, []agent.Signal) (agent.Transition, error) { return agent.Transition{}, nil }}
			})
		}, "Step returned an invalid Transition"},
		{"consumption beyond admitted signals", func(probe *definitionProbe) {
			probe.decorateStart(func(execution agent.Execution) agent.Execution {
				return &executionProbe{snapshot: execution.Snapshot, step: func(context.Context, []agent.Signal) (agent.Transition, error) { return agent.Continue(1) }}
			})
		}, "consumed 1 Signals from a prefix of 0"},
		{"step failure", func(probe *definitionProbe) {
			probe.decorateStart(func(execution agent.Execution) agent.Execution {
				return &executionProbe{snapshot: execution.Snapshot, step: func(context.Context, []agent.Signal) (agent.Transition, error) { return agent.Transition{}, cause }}
			})
		}, "Execution.Step: fixture callback refused"},
		{"descriptor lost after execution", func(probe *definitionProbe) {
			describe := probe.describe
			var calls atomic.Uint64
			probe.describe = func() agent.Descriptor {
				if calls.Add(1) > 1 {
					return agent.Descriptor{}
				}
				return describe()
			}
		}, "invalid Descriptor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := newDefinitionConformanceFixture(t)
			probe := &definitionProbe{describe: base.Descriptor, start: base.Start, restore: base.Restore}
			test.configure(probe)
			input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
			if err != nil {
				t.Fatal(err)
			}
			err = verifyFreshExecutions(t.Context(), DefinitionConformanceConfig{Definition: probe, Input: input})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("broken lifecycle accepted or misreported: %v, want %q", err, test.want)
			}
			if strings.Contains(test.want, cause.Error()) && !errors.Is(err, cause) {
				t.Fatalf("conformance lost the callback cause: %v", err)
			}
		})
	}
}

func TestDefinitionConformanceRejectsUnusableInputsAndRestoredCases(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []DefinitionConformanceConfig{
		{Definition: (*definitionProbe)(nil), Input: input},
		{Definition: definition},
		{Definition: definition, Input: input, RestoredCases: []ExecutionConformanceCase{{Name: "missing state"}}},
	} {
		if validationErr := validateDefinitionConformanceConfig(config); validationErr == nil {
			t.Fatal("unusable conformance configuration was accepted")
		}
	}
	wrong, err := agent.EncodePayload("outside the declared schema")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyFreshExecutions(t.Context(), DefinitionConformanceConfig{Definition: definition, Input: wrong}); !errors.Is(err, agent.ErrInvalidPayload) {
		t.Fatalf("input schema mismatch = %v", err)
	}
	var typedNil *executionProbe
	if err := verifyExecutionStep(t.Context(), definition, typedNil, typedNil, nil); err == nil || !strings.Contains(err.Error(), "nil Execution") {
		t.Fatalf("nil execution pair = %v", err)
	}
}

func TestRestorationConformanceRejectsUnfaithfulOrFailedRecovery(t *testing.T) {
	cause := errors.New("restore fixture unavailable")
	for _, test := range []struct {
		name      string
		configure func(*definitionProbe, agent.ExecutionState)
		want      string
	}{
		{"first restore fails", func(probe *definitionProbe, _ agent.ExecutionState) {
			probe.restore = func(context.Context, agent.ExecutionState) (agent.Execution, error) { return nil, cause }
		}, "Definition.Restore"},
		{"second restore fails", func(probe *definitionProbe, _ agent.ExecutionState) {
			restore, calls := probe.restore, 0
			probe.restore = func(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
				calls++
				if calls == 2 {
					return nil, cause
				}
				return restore(ctx, state)
			}
		}, "Definition.Restore"},
		{"restored snapshot fails", func(probe *definitionProbe, _ agent.ExecutionState) {
			probe.restore = func(context.Context, agent.ExecutionState) (agent.Execution, error) {
				return &executionProbe{snapshot: func() (agent.ExecutionState, error) { return agent.ExecutionState{}, cause }}, nil
			}
		}, "Execution.Snapshot"},
		{"restoration rewrites state", func(probe *definitionProbe, state agent.ExecutionState) {
			changed, err := agent.EncodeExecutionState(state.Kind(), struct {
				Value int `json:"value"`
			}{Value: 99})
			if err != nil {
				t.Fatal(err)
			}
			probe.restore = func(context.Context, agent.ExecutionState) (agent.Execution, error) {
				return &executionProbe{snapshot: func() (agent.ExecutionState, error) { return changed, nil }}, nil
			}
		}, "restored state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := newDefinitionConformanceFixture(t)
			input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
			if err != nil {
				t.Fatal(err)
			}
			execution, err := base.Start(input)
			if err != nil {
				t.Fatal(err)
			}
			state, err := execution.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			probe := &definitionProbe{describe: base.Descriptor, start: base.Start, restore: base.Restore}
			test.configure(probe, state)
			err = verifyRestoredExecutions(t.Context(), probe, ExecutionConformanceCase{State: state})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid restore accepted: %v, want %s", err, test.want)
			}
		})
	}
}

func TestDescriptorConformanceRejectsConcurrentContractChanges(t *testing.T) {
	base := newDefinitionConformanceFixture(t)
	var calls atomic.Uint64
	probe := &definitionProbe{describe: func() agent.Descriptor {
		if calls.Add(1) == 1 {
			return base.Descriptor()
		}
		return agent.Descriptor{}
	}}
	if err := verifyDescriptorStability(probe); err == nil {
		t.Fatal("concurrent invalid descriptor was accepted")
	}
}

func TestConformanceReportsFailuresAtEachExecutionComparisonBoundary(t *testing.T) {
	cause := errors.New("comparison fixture unavailable")
	for _, test := range []struct {
		name       string
		snapshotAt int
		rightStep  bool
		restored   bool
		different  bool
		want       string
	}{
		{name: "initial sibling snapshot", snapshotAt: 1, want: cause.Error()},
		{name: "sibling snapshot after first step", snapshotAt: 2, want: cause.Error()},
		{name: "final sibling snapshot", snapshotAt: 3, want: cause.Error()},
		{name: "sibling step fails", rightStep: true, want: cause.Error()},
		{name: "sibling transition differs", rightStep: true, different: true, want: "Step Transition"},
		{name: "restored step fails", restored: true, want: cause.Error()},
		{name: "restored transition differs", restored: true, different: true, want: "original versus restored Step Transition"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := newDefinitionConformanceFixture(t)
			input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
			if err != nil {
				t.Fatal(err)
			}
			left, err := base.Start(input)
			if err != nil {
				t.Fatal(err)
			}
			right, err := base.Start(input)
			if err != nil {
				t.Fatal(err)
			}
			probe := &definitionProbe{describe: base.Descriptor, start: base.Start, restore: base.Restore}
			badStep := func(context.Context, []agent.Signal) (agent.Transition, error) {
				if test.different {
					return agent.Complete(0, input)
				}
				return agent.Transition{}, cause
			}
			if test.snapshotAt != 0 {
				calls, snapshot := 0, right.Snapshot
				right = &executionProbe{step: right.Step, snapshot: func() (agent.ExecutionState, error) {
					calls++
					if calls == test.snapshotAt {
						return agent.ExecutionState{}, cause
					}
					return snapshot()
				}}
			}
			if test.rightStep {
				right = &executionProbe{snapshot: right.Snapshot, step: badStep}
			}
			if test.restored {
				probe.restore = func(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
					execution, restoreErr := base.Restore(ctx, state)
					if restoreErr != nil {
						return nil, restoreErr
					}
					return &executionProbe{snapshot: execution.Snapshot, step: badStep}, nil
				}
			}
			err = verifyExecutionStep(t.Context(), probe, left, right, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("comparison defect was accepted or misreported: %v, want %s", err, test.want)
			}
			if !test.different && !errors.Is(err, cause) {
				t.Fatalf("comparison lost the original callback cause: %v", err)
			}
		})
	}
}

func TestConformanceRejectsEqualTransitionsWithDifferentResultingState(t *testing.T) {
	definition := newDefinitionConformanceFixture(t)
	input, err := agent.EncodePayload(definitionConformanceInput{Value: 7})
	if err != nil {
		t.Fatal(err)
	}
	left, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	right, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	stepped := false
	changed := &executionProbe{snapshot: right.Snapshot, step: func(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
		stepped = true
		return right.Step(ctx, signals)
	}}
	changed.snapshot = func() (agent.ExecutionState, error) {
		if stepped {
			return agent.EncodeExecutionState("agenttest.definition_conformance", definitionConformanceInput{Value: 99})
		}
		return right.Snapshot()
	}
	err = verifyExecutionStep(t.Context(), definition, left, changed, nil)
	if !errors.Is(err, errConformanceValuesDiffer) || !strings.Contains(err.Error(), "resulting Execution state") {
		t.Fatalf("equal Transitions hid divergent resulting state: %v", err)
	}
}
