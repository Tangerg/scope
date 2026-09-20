// Package conformancetest captures real Engine boundaries for built-in Strategy tests.
package conformancetest

import (
	"context"
	"errors"
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
	input agent.Payload,
) agent.Result {
	t.Helper()
	definition := deploymentConfig.Definition
	recorder := &recordingDefinition{definition: definition, frameReady: make(chan struct{}), releaseFrame: make(chan struct{})}
	release := sync.OnceFunc(func() { close(recorder.releaseFrame) })
	defer release()
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
	process, err := engine.Start(t.Context(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- process.Join(t.Context()) }()
	select {
	case <-recorder.frameReady:
	case joinErr := <-finished:
		t.Fatalf("execution ended before exercising a protocol frame: %v", joinErr)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	id, err := agent.ParseSignalID("signal:conformance-unsupported")
	if err != nil {
		t.Fatal(err)
	}
	request, err := agent.NewSignalRequest(id, agent.WaitID{}, []byte(`{"unsupported":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliveryErr := process.DeliverSignals(t.Context(), request); accepted || !errors.Is(deliveryErr, agent.ErrSignalRejected) {
		t.Fatalf("unsupported Signal admission=%t %v", accepted, deliveryErr)
	}
	after, err := engine.InspectTree(t.Context(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	current, found := after.Process(process.ID())
	if !found {
		t.Fatal("rejected input removed the Process")
	}
	for _, receipt := range current.Snapshot.SignalReceipts() {
		if receipt.ID() == id {
			t.Fatal("rejected input was admitted")
		}
	}
	release()
	if joinErr := <-finished; joinErr != nil {
		t.Fatal(joinErr)
	}
	result, err := process.Await(t.Context())
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("capture result status=%s termination=%+v error=%v", result.Status(), result.Termination(), err)
	}
	recorder.mu.Lock()
	cases := slices.Clone(recorder.cases)
	recorder.mu.Unlock()
	if len(cases) < 2 {
		t.Fatal("conformance scenario must exercise a continuation boundary")
	}
	for _, sample := range cases {
		CheckRestoreCancellation(t, definition, sample.State)
	}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{
		Definition: definition, Input: input, RestoredCases: cases,
	})
	return result
}

type recordingDefinition struct {
	definition   agent.Definition
	mu           sync.Mutex
	cases        []agenttest.ExecutionConformanceCase
	frameReady   chan struct{}
	releaseFrame chan struct{}
	frameOnce    sync.Once
}

func (r *recordingDefinition) Descriptor() agent.Descriptor { return r.definition.Descriptor() }

func (r *recordingDefinition) Start(input agent.Payload) (agent.Execution, error) {
	execution, err := r.definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &recordingExecution{execution: execution, recorder: r}, nil
}

func (r *recordingDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	execution, err := r.definition.Restore(ctx, state)
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
	if len(signals) > 0 {
		r.recorder.frameOnce.Do(func() {
			close(r.recorder.frameReady)
			select {
			case <-r.recorder.releaseFrame:
			case <-ctx.Done():
			}
		})
	}
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

var _ agent.Definition = (*recordingDefinition)(nil)
