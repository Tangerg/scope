// Package conformancetest captures real Engine boundaries for built-in Strategy tests.
package conformancetest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

// Run records a completed execution and verifies every captured Step through
// the public conformance suite. Signals come from the Engine, so framework
// identities and Strategy protocol samples cannot drift from their producers.
func Run(
	t *testing.T,
	deploymentConfig agent.DeploymentConfig,
	engineConfig agent.EngineConfig,
	input agent.Input,
) agent.Result {
	t.Helper()
	definition := deploymentConfig.Definition
	recorder := &recordingDefinition{definition: definition}
	deploymentConfig.Definition = recorder
	deployment, err := agent.NewDeployment(deploymentConfig)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(engineConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("capture result status=%s termination=%+v error=%v", result.Status(), result.Termination(), err)
	}
	recorder.mu.Lock()
	cases := slices.Clone(recorder.cases)
	recorder.mu.Unlock()
	if len(cases) < 2 {
		t.Fatal("conformance scenario must exercise a continuation boundary")
	}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{
		Definition: definition, Input: input, RestoredCases: cases,
	})
	return result
}

type recordingDefinition struct {
	definition agent.Definition
	mu         sync.Mutex
	cases      []agenttest.ExecutionConformanceCase
}

func (r *recordingDefinition) Descriptor() agent.Descriptor { return r.definition.Descriptor() }

func (r *recordingDefinition) Start(input agent.Input) (agent.Execution, error) {
	execution, err := r.definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{execution: execution, recorder: r}, nil
}

func (r *recordingDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	execution, err := r.definition.Restore(state)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{execution: execution, recorder: r}, nil
}

type recordingExecution struct {
	execution agent.Execution
	recorder  *recordingDefinition
}

func (r *recordingExecution) Snapshot() (agent.ExecutionState, error) {
	return r.execution.Snapshot()
}

func (r *recordingExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	state, err := r.execution.Snapshot()
	if err != nil {
		return agent.Transition{}, err
	}
	transition, err := r.execution.Step(ctx, signals)
	if err != nil {
		return agent.Transition{}, err
	}
	r.recorder.mu.Lock()
	defer r.recorder.mu.Unlock()
	r.recorder.cases = append(r.recorder.cases, agenttest.ExecutionConformanceCase{
		Name: fmt.Sprintf("step_%02d", len(r.recorder.cases)+1), State: state, Signals: slices.Clone(signals),
	})
	return transition, nil
}
