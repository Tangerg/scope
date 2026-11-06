package planning_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/run"
)

func TestPlanningSensorWriterStopPreservesSameIdentityReplay(t *testing.T) {
	for _, sample := range []struct {
		name         string
		cancellation func(context.Context) error
	}{
		{"context error", func(ctx context.Context) error { return ctx.Err() }},
		{"context cause", context.Cause},
	} {
		t.Run(sample.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			store := run.NewMemoryStore()
			original := conformancetest.Runtime(t, run.Config{Store: store})
			done, goal, _ := domain()
			requests := make(chan planning.SenseRequest, 2)
			var calls atomic.Int32
			sensor := planning.SensorFunc(func(ctx context.Context, request planning.SenseRequest) (planning.WorldState, error) {
				requests <- request
				if calls.Add(1) == 1 {
					<-ctx.Done()
					return planning.WorldState{}, fmt.Errorf("sensor interrupted: %w", sample.cancellation(ctx))
				}
				return planning.NewWorldState(done)
			})
			program := require(planning.Compile[string](original.Compiler(), baseConfig(goal, sensor)))
			handle := require(original.Start(ctx, program, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: "sensor-replay"}, Input: "observe"}))
			first := <-requests
			if err := original.Close(ctx); err != nil {
				t.Fatal(err)
			}
			replacement := conformancetest.Runtime(t, run.Config{Store: store})
			recovered := require(replacement.Recover(ctx, program, handle.ID()))
			inspection := require(recovered.Inspect(ctx))
			process, found := inspection.Process(handle.ID())
			if !found || len(process.PendingEffects) != 1 || process.PendingEffects[0].Outcome.Status() != run.SettlementStatusUnknown {
				t.Fatalf("local writer shutdown became a definite sensing outcome: %+v", process)
			}
			if err := recovered.ReplayUnknownEffect(ctx, first.EffectID); err != nil {
				t.Fatal(err)
			}
			second := <-requests
			if second.EffectID != first.EffectID || calls.Load() != 2 {
				t.Fatal("sensor replay changed effect identity or made another attempt")
			}
			result := require(recovered.Await(ctx))
			if output := require(result.Output()); output.Outcome != planning.OutcomeAchieved {
				t.Fatalf("replayed observation did not establish the goal: %+v", output)
			}
		})
	}
}

func TestPlanningSensorWriterStopRetainsDefiniteResults(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "observed world"
		if failed {
			name = "definite sensor error"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			store := run.NewMemoryStore()
			original := conformancetest.Runtime(t, run.Config{Store: store})
			done, goal, _ := domain()
			entered := make(chan struct{})
			cause := errors.New("sensor access denied")
			sensor := planning.SensorFunc(func(ctx context.Context, _ planning.SenseRequest) (planning.WorldState, error) {
				close(entered)
				<-ctx.Done()
				if failed {
					return planning.WorldState{}, cause
				}
				return planning.NewWorldState(done)
			})
			program := require(planning.Compile[string](original.Compiler(), baseConfig(goal, sensor)))
			handle := require(original.Start(ctx, program, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: "definite-result"}, Input: "observe"}))
			<-entered
			if err := original.Close(ctx); err != nil {
				t.Fatal(err)
			}
			replacement := conformancetest.Runtime(t, run.Config{Store: store})
			recovered := require(replacement.Recover(ctx, program, handle.ID()))
			result := require(recovered.Await(ctx))
			if failed {
				failure, present := result.Termination().Failure()
				if !present || failure.Kind() != run.FailureKindExternal || failure.Message() != cause.Error() {
					t.Fatalf("definite sensor failure was lost: %s %v", result.Termination().Status(), failure)
				}
				return
			}
			if output := require(result.Output()); output.Outcome != planning.OutcomeAchieved {
				t.Fatalf("definite observation was lost: %+v", output)
			}
		})
	}
}

func TestPlanningSensorBusinessCancellationDrains(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime := conformancetest.Runtime(t, run.Config{})
	_, goal, _ := domain()
	entered := make(chan struct{})
	sensor := planning.SensorFunc(func(ctx context.Context, _ planning.SenseRequest) (planning.WorldState, error) {
		close(entered)
		<-ctx.Done()
		return planning.WorldState{}, ctx.Err()
	})
	program := require(planning.Compile[string](runtime.Compiler(), baseConfig(goal, sensor)))
	handle := require(runtime.Start(ctx, program, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: t.Name()}, Input: "observe"}))
	<-entered
	if err := handle.Cancel(ctx, "cancel the planning process"); err != nil {
		t.Fatal(err)
	}
	result := require(handle.Await(ctx))
	if result.Termination().Status() != run.StatusCanceled {
		t.Fatalf("business cancellation became unresolved or failed: %s", result.Termination().Status())
	}
}
