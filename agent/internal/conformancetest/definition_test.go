package conformancetest

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
)

type recordingFailureDefinition struct{ cause error }

func (r recordingFailureDefinition) Descriptor() agent.Descriptor { return agent.Descriptor{} }
func (r recordingFailureDefinition) Start(agent.Payload) (agent.Execution, error) {
	return nil, r.cause
}
func (r recordingFailureDefinition) Restore(context.Context, agent.ExecutionState) (agent.Execution, error) {
	return nil, r.cause
}

type recordingFailureExecution struct {
	state       agent.ExecutionState
	snapshotErr error
	stepErr     error
}

func (r recordingFailureExecution) Snapshot() (agent.ExecutionState, error) {
	return r.state, r.snapshotErr
}
func (r recordingFailureExecution) Step(ctx context.Context, _ []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	return agent.Transition{}, r.stepErr
}

func TestRecorderDoesNotPromoteFailedCallbacksToConformanceCases(t *testing.T) {
	cause := errors.New("callback failed")
	definition := &recordingDefinition{definition: recordingFailureDefinition{cause: cause}}
	if _, err := definition.Start(agent.Payload{}); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := definition.Restore(t.Context(), agent.ExecutionState{}); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	state, err := agent.EncodeExecutionState("test", 1)
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
	if _, err := execution.Step(ctx, []agent.Signal{{}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(definition.cases) != 0 {
		t.Fatal("canceled frame entered the replay cases")
	}
}
