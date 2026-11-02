// Package conformancetest captures runtime turns for deterministic strategy tests.
package conformancetest

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

const Build = "strategy-conformance"

func Runtime(t testing.TB, config run.Config) *run.Runtime {
	t.Helper()
	if config.Store == nil {
		config.Store = run.NewMemoryStore()
	}
	if config.Build == "" {
		config.Build = Build
	}
	runtime, err := run.New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return runtime
}
func Compiler(t testing.TB) *run.Compiler { t.Helper(); return Runtime(t, run.Config{}).Compiler() }

func Run(t *testing.T, binding run.BindingConfig, config run.Config, input run.Payload) run.Result[run.Payload] {
	t.Helper()
	runtime := Runtime(t, config)
	original := binding.Definition
	recorder := &recordingDefinition{definition: original}
	binding.Definition = recorder
	program, err := run.Bind[run.Payload, run.Payload](runtime.Compiler(), binding)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "conformance", Key: rand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handle.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("result %s: %v", result.Termination().Status(), err)
	}
	cases := recorder.recordedCases()
	if len(cases) == 0 {
		t.Fatal("no runtime turns recorded")
	}
	for _, sample := range cases {
		CheckRestoreCancellation(t, original, sample.State)
	}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{Definition: original, Input: input, InitialTurn: cases[0].Turn, RestoredCases: cases})
	return result
}

type recordingDefinition struct {
	definition run.Definition
	mu         sync.Mutex
	cases      []runtest.ExecutionConformanceCase
}

func (r *recordingDefinition) Descriptor() run.Descriptor { return r.definition.Descriptor() }
func (r *recordingDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return r.definition.ChildPrograms()
}
func (r *recordingDefinition) Start(input run.Payload) (run.Execution, error) {
	execution, err := r.definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{execution: execution, recorder: r}, nil
}
func (r *recordingDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	execution, err := r.definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{execution: execution, recorder: r}, nil
}
func (r *recordingDefinition) record(state run.ExecutionState, turn run.Turn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	turn.Messages = slices.Clone(turn.Messages)
	r.cases = append(r.cases, runtest.ExecutionConformanceCase{Name: fmt.Sprintf("step_%d", turn.Step), State: state, Turn: turn})
}
func (r *recordingDefinition) recordedCases() []runtest.ExecutionConformanceCase {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.cases)
}

type recordingExecution struct {
	execution run.Execution
	recorder  *recordingDefinition
}

func (r *recordingExecution) Snapshot() (run.ExecutionState, error) { return r.execution.Snapshot() }
func (r *recordingExecution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	state, err := r.execution.Snapshot()
	if err != nil {
		return run.Transition{}, err
	}
	transition, err := r.execution.Step(ctx, turn)
	if err != nil {
		return run.Transition{}, err
	}
	r.recorder.record(state, turn)
	return transition, nil
}
