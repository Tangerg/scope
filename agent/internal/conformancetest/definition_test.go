package conformancetest

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

type recordingFailureDefinition struct{ cause error }

func (r recordingFailureDefinition) Descriptor() run.Descriptor { return run.Descriptor{} }

func (recordingFailureDefinition) ChildDeployments() []run.Program[run.Payload, run.Payload] {
	return nil
}
func (r recordingFailureDefinition) Start(run.Payload) (run.Execution, error) {
	return nil, r.cause
}
func (r recordingFailureDefinition) Restore(context.Context, run.ExecutionState) (run.Execution, error) {
	return nil, r.cause
}

type recordingFailureExecution struct {
	state       run.ExecutionState
	snapshotErr error
	stepErr     error
}

func (r recordingFailureExecution) Snapshot() (run.ExecutionState, error) {
	return r.state, r.snapshotErr
}
func (r recordingFailureExecution) Step(ctx context.Context, _ []run.Signal) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	return run.Transition{}, r.stepErr
}

func TestRecorderDoesNotPromoteFailedCallbacksToConformanceCases(t *testing.T) {
	cause := errors.New("callback failed")
	definition := &recordingDefinition{definition: recordingFailureDefinition{cause: cause}}
	if _, err := definition.Start(run.Payload{}); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := definition.Restore(t.Context(), run.ExecutionState{}); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	state, err := run.EncodeExecutionState("test", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []recordingFailureExecution{{snapshotErr: cause}, {state: state, stepErr: cause}} {
		execution := &recordingExecution{execution: fixture, recorder: definition}
		if _, err := execution.Step(t.Context(), nil); !errors.Is(err, cause) {
			t.Fatal(err)
		}
		if len(definition.cases) != 0 {
			t.Fatal("failed Step entered the replay cases")
		}
	}
	definition.frameReady, definition.releaseFrame = make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	execution := &recordingExecution{execution: recordingFailureExecution{state: state}, recorder: definition}
	if _, err := execution.Step(ctx, []run.Signal{{}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(definition.cases) != 0 {
		t.Fatal("canceled frame entered the replay cases")
	}
}
