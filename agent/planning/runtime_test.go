package planning_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/planning/goap"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

func require[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

type observedWorld struct {
	mu           sync.Mutex
	state        planning.WorldState
	observations int
}

func (o *observedWorld) Sense(context.Context, planning.SenseRequest) (planning.WorldState, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.observations++
	return o.state, nil
}
func (o *observedWorld) apply(effect planning.Condition) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.state = require(o.state.Apply(effect))
}

func domain() (planning.Condition, planning.Goal, planning.Action) {
	done := require(planning.NewCondition("world.done", planning.TruthTrue))
	goal := require(planning.NewGoal(planning.GoalConfig{Name: "goal.done", Description: "Finish the observed work.", Conditions: []planning.Condition{done}}))
	action := require(planning.NewAction(planning.ActionConfig{Name: "finish", Description: "Attempt to finish the work.", Effects: []planning.Condition{done}}))
	return done, goal, action
}
func baseConfig(goal planning.Goal, world planning.Sensor, bindings ...planning.ActionBinding) planning.Config {
	return planning.Config{Name: "test.planning", Description: "Plan against observed truth.", Revision: "test-planning", Goal: goal, Actions: bindings, Planner: goap.New(goap.Config{}), Sensor: world, MaxActionAttempts: run.NewQuota(3)}
}

func TestPlanningConfirmsEffectsThroughSensor(t *testing.T) {
	for _, observed := range []bool{true, false} {
		t.Run(map[bool]string{true: "observed", false: "unconfirmed"}[observed], func(t *testing.T) {
			runtime := conformancetest.Runtime(t, run.Config{})
			done, goal, action := domain()
			world := &observedWorld{}
			binding := require(planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: action}))
			config := baseConfig(goal, world, binding)
			config.ActionExecutors = map[string]planning.ActionExecutor{action.Name(): planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
				if observed {
					world.apply(done)
				}
				return planning.ActionSucceeded(), nil
			})}
			program := require(planning.Compile[string](runtime.Compiler(), config))
			output := require(runtime.Run(t.Context(), program, "work"))
			expectedOutcome, expectedAttempt := planning.OutcomeStuck, planning.AttemptUnconfirmed
			if observed {
				expectedOutcome, expectedAttempt = planning.OutcomeAchieved, planning.AttemptSucceeded
			}
			if output.Outcome != expectedOutcome || len(output.Attempts) != 1 || output.Attempts[0].Status != expectedAttempt || world.observations != 2 {
				t.Fatalf("output=%+v observations=%d", output, world.observations)
			}
		})
	}
}

func TestPlanningAlreadySatisfiedAndBoundedAttempts(t *testing.T) {
	for _, already := range []bool{true, false} {
		t.Run(map[bool]string{true: "already satisfied", false: "finite bound"}[already], func(t *testing.T) {
			runtime := conformancetest.Runtime(t, run.Config{})
			done, goal, action := domain()
			world := &observedWorld{}
			if already {
				world.apply(done)
			}
			binding := require(planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: action}))
			config := baseConfig(goal, world, binding)
			config.MaxActionAttempts = run.NewQuota(1)
			config.ActionExecutors = map[string]planning.ActionExecutor{action.Name(): planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
				return require(planning.ActionFailed("definite refusal")), nil
			})}
			program := require(planning.Compile[string](runtime.Compiler(), config))
			output := require(runtime.Run(t.Context(), program, "work"))
			if already {
				if output.Outcome != planning.OutcomeAchieved || len(output.Attempts) != 0 {
					t.Fatal(output)
				}
			} else if output.Outcome != planning.OutcomeExhausted || len(output.Attempts) != 1 || output.Attempts[0].Status != planning.AttemptFailed {
				t.Fatal(output)
			}
		})
	}
}

func TestPlanningActionUncertaintyIsNotReexecuted(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	done, goal, action := domain()
	world := &observedWorld{}
	var calls atomic.Int32
	binding := require(planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: action}))
	config := baseConfig(goal, world, binding)
	config.ActionExecutors = map[string]planning.ActionExecutor{action.Name(): planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
		calls.Add(1)
		world.apply(done)
		return planning.ActionResult{}, errors.New("acknowledgment lost")
	})}
	program := require(planning.Compile[string](runtime.Compiler(), config))
	handle := require(runtime.Start(t.Context(), program, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: t.Name()}, Input: "work"}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var effect run.EffectID
	for !effect.Valid() {
		view := require(handle.Inspect(ctx))
		process, _ := view.Process(handle.ID())
		for _, pending := range process.PendingEffects {
			if pending.Outcome.Status() == run.SettlementStatusUnknown {
				effect = pending.Request.ID()
				if pending.Policy.Replay != run.ReplayPolicyNever {
					t.Fatal("uncertain action can replay")
				}
			}
		}
		if effect.Valid() {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if err := handle.ResolveUnknownEffect(ctx, effect, require(planning.NewActionSettlement(planning.ActionSucceeded()))); err != nil {
		t.Fatal(err)
	}
	result := require(handle.Await(ctx))
	output := require(result.Output())
	if output.Outcome != planning.OutcomeAchieved || calls.Load() != 1 {
		t.Fatalf("output=%+v calls=%d", output, calls.Load())
	}
}

func TestPlanningChildActionUsesExplicitExitAndReobserves(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	done, goal, action := domain()
	world := &observedWorld{}
	childStage := require(workflow.Transform("finish", func(_ context.Context, value string) (string, error) { return value, nil }))
	child := require(workflow.Compile[string, string](runtime.Compiler(), workflow.Config{Name: "test.child", Description: "Perform the child action.", Revision: "identity", Stages: []workflow.Stage{childStage}}))
	binding := require(planning.NewChildBinding(planning.ChildBindingConfig{Action: action, Program: child.Erase()}))
	sensor := planning.SensorFunc(func(ctx context.Context, request planning.SenseRequest) (planning.WorldState, error) {
		world.mu.Lock()
		if world.observations > 0 {
			world.state = require(world.state.Apply(done))
		}
		world.mu.Unlock()
		return world.Sense(ctx, request)
	})
	program := require(planning.Compile[string](runtime.Compiler(), baseConfig(goal, sensor, binding)))
	handle := require(runtime.Start(t.Context(), program, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: t.Name()}, Input: "work"}))
	result := require(handle.Await(t.Context()))
	output := require(result.Output())
	if output.Outcome != planning.OutcomeAchieved || len(output.Attempts) != 1 {
		t.Fatal(output)
	}
	log, _, err := store.Load(t.Context(), handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	spawned := 0
	for _, entry := range log.Entries {
		if started, ok := entry.Record.(run.Started); ok && started.Process.Depth() > 0 {
			spawned++
		}
	}
	if spawned != 1 || len(require(log.Inspect()).Processes) != 1 {
		t.Fatal("child lifecycle not consumed and reaped")
	}
}

func TestPlanningRejectsIncompleteExecutorBindingsAndOpaqueIdentity(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	_, goal, action := domain()
	binding := require(planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: action}))
	config := baseConfig(goal, &observedWorld{}, binding)
	if _, err := planning.Compile[string](runtime.Compiler(), config); !errors.Is(err, planning.ErrInvalidDispatcherConfig) {
		t.Fatalf("missing executor accepted: %v", err)
	}
	config.ActionExecutors = map[string]planning.ActionExecutor{action.Name(): planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
		return planning.ActionSucceeded(), nil
	})}
	config.Revision = ""
	if _, err := planning.Compile[string](runtime.Compiler(), config); !errors.Is(err, run.ErrInvalidProgram) {
		t.Fatalf("opaque configuration without revision: %v", err)
	}
}
