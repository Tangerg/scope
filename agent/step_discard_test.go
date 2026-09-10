package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestPauseReportsCommittedStateRestorationFailure(t *testing.T) {
	for _, durable := range []bool{false, true} {
		name := "ephemeral"
		if durable {
			name = "durable"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				config := EngineConfig{}
				if durable {
					config.TreeDurability = &recordingTreeDurability{}
				}
				engine, err := NewEngine(config)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { mustCloseEngine(t, engine) })
				cause := errors.New("committed state reconstruction failed")
				definition := &discardFailureDefinition{
					Definition: newEngineTestDefinition(t, "engine.effect", "effect"),
					entered:    make(chan struct{}), restoreErr: cause,
				}
				deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
				input, err := EncodeInput(engineTestInput{Value: "original state"})
				if err != nil {
					t.Fatal(err)
				}
				process, err := engine.Start(t.Context(), deployment, input)
				if err != nil {
					t.Fatal(err)
				}
				<-definition.entered
				if pauseErr := process.Pause(t.Context(), "discard active Step"); pauseErr != nil {
					t.Fatal(pauseErr)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				result, err := process.Await(ctx)
				if err != nil {
					t.Fatalf("restoration failure left the Process paused without an Execution: %v", err)
				}
				failure, failed := result.Termination().Failure()
				if result.Status() != StatusFailed || !failed || failure.Kind() != FailureKindExecution ||
					failure.Code() != "execution.snapshot.unrestorable" || failure.Message() != cause.Error() {
					t.Fatalf("restoration failure was lost: result=%s failure=%+v", result.Status(), failure)
				}
				if result.Usage() != (Usage{}) {
					t.Fatalf("discarded Step changed usage: %+v", result.Usage())
				}
				if joinErr := process.Join(ctx); joinErr != nil {
					t.Fatal(joinErr)
				}
			})
		})
	}
}

type discardFailureDefinition struct {
	Definition
	entered    chan struct{}
	restoreErr error
	restores   atomic.Uint32
}

func (d *discardFailureDefinition) Restore(state ExecutionState) (Execution, error) {
	if d.restores.Add(1) > 1 {
		return nil, d.restoreErr
	}
	execution, err := d.Definition.Restore(state)
	if err != nil {
		return nil, err
	}
	return &discardFailureExecution{Execution: execution, entered: d.entered}, nil
}

type discardFailureExecution struct {
	Execution
	entered chan struct{}
}

func (d *discardFailureExecution) Step(ctx context.Context, _ []Signal) (Transition, error) {
	close(d.entered)
	<-ctx.Done()
	return Transition{}, ctx.Err()
}
