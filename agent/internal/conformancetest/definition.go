// Package conformancetest captures real Runtime boundaries for built-in Strategy tests.
package conformancetest

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

// Run records a completed execution and verifies every captured Step through
// the public conformance suite. Signals come from the Runtime, so framework
// identities and Strategy protocol samples cannot drift from their producers.
func Run(
	t *testing.T,
	deploymentConfig run.BindingConfig,
	engineConfig run.Config,
	input run.Payload,
) run.Result[run.Payload] {
	t.Helper()
	definition := deploymentConfig.Definition
	recorder := &recordingDefinition{definition: definition, frameReady: make(chan struct{}), releaseFrame: make(chan struct{})}
	release := sync.OnceFunc(func() { close(recorder.releaseFrame) })
	defer release()
	deploymentConfig.Definition = recorder
	deployment, err := run.Bind[run.Payload, run.Payload](deploymentConfig)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := run.New(context.Background(), engineConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- process.Join(t.Context()) }()
	recorder.awaitFrame(t, finished)
	assertUnsupportedSignalRejected(t, engine, process)
	release()
	if joinErr := <-finished; joinErr != nil {
		t.Fatal(joinErr)
	}
	result, err := process.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("capture result status=%s termination=%+v error=%v", result.Termination().Status(), result.Termination(), err)
	}
	cases := recorder.recordedCases()
	if len(cases) < 2 {
		t.Fatal("conformance scenario must exercise a continuation boundary")
	}
	for _, sample := range cases {
		CheckRestoreCancellation(t, definition, sample.State)
	}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{
		Definition: definition, Input: input, RestoredCases: cases,
	})
	return result
}

func assertUnsupportedSignalRejected(t *testing.T, engine *run.Runtime, process *run.Handle[run.Payload]) {
	t.Helper()
	id, err := run.ParseSignalID("signal:conformance-unsupported")
	if err != nil {
		t.Fatal(err)
	}
	request, err := run.NewSignalRequest(id, run.WaitID{}, []byte(`{"unsupported":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliveryErr := process.DeliverSignals(t.Context(), request); accepted || !errors.Is(deliveryErr, run.ErrSignalRejected) {
		t.Fatalf("unsupported Signal admission=%t %v", accepted, deliveryErr)
	}
	after, err := engine.InspectTree(t.Context(), process.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	current, found := after.Process(process.Relation().ProcessID())
	if !found {
		t.Fatal("rejected input removed the Process")
	}
	for _, receipt := range current.Snapshot.SignalReceipts() {
		if receipt.ID() == id {
			t.Fatal("rejected input was admitted")
		}
	}
}

type recordingDefinition struct {
	definition   run.Definition
	mu           sync.Mutex
	cases        []runtest.ExecutionConformanceCase
	frameReady   chan struct{}
	releaseFrame chan struct{}
	frameOnce    sync.Once
}

func (r *recordingDefinition) Descriptor() run.Descriptor { return r.definition.Descriptor() }

func (r *recordingDefinition) ChildDeployments() []run.Program[run.Payload, run.Payload] {
	return r.definition.ChildDeployments()
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

// holdFirstFrame parks the first Step that receives Signals until Run releases
// it, giving Run a deterministic window in which the Process is still live.
func (r *recordingDefinition) holdFirstFrame(ctx context.Context) {
	r.frameOnce.Do(func() {
		close(r.frameReady)
		select {
		case <-r.releaseFrame:
		case <-ctx.Done():
		}
	})
}

func (r *recordingDefinition) awaitFrame(t *testing.T, finished <-chan error) {
	t.Helper()
	select {
	case <-r.frameReady:
	case joinErr := <-finished:
		t.Fatalf("execution ended before exercising a protocol frame: %v", joinErr)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

func (r *recordingDefinition) record(state run.ExecutionState, signals []run.Signal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cases = append(r.cases, runtest.ExecutionConformanceCase{
		Name: fmt.Sprintf("step_%02d", len(r.cases)+1), State: state, Signals: slices.Clone(signals),
	})
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

func (r *recordingExecution) Snapshot() (run.ExecutionState, error) {
	return r.execution.Snapshot()
}

func (r *recordingExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if len(signals) > 0 {
		r.recorder.holdFirstFrame(ctx)
	}
	state, err := r.execution.Snapshot()
	if err != nil {
		return run.Transition{}, err
	}
	transition, err := r.execution.Step(ctx, signals)
	if err != nil {
		return run.Transition{}, err
	}
	r.recorder.record(state, signals)
	return transition, nil
}

var _ run.Definition = (*recordingDefinition)(nil)
