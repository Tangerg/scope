package planning_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/run"
)

func TestCompileDerivesPlanningInputAndPinsOpaqueBindings(t *testing.T) {
	ready, err := planning.NewCondition("ready", planning.TruthTrue)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := planning.NewGoal(planning.GoalConfig{Name: "ready", Description: "Observe readiness.", Conditions: []planning.Condition{ready}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := planning.Config{Name: "test.ready", Description: "Return an already-satisfied goal.", Goal: goal,
		Planner: planning.PlannerFunc(func(context.Context, planning.Problem) (planning.Plan, bool, error) {
			t.Error("planning a satisfied goal")
			return planning.Plan{}, false, nil
		}),
		Sensor: planning.SensorFunc(func(context.Context, planning.SenseRequest) (planning.WorldState, error) {
			return planning.NewWorldState(ready)
		}),
		BuildIdentity: run.ComputeDigest([]byte("ready.bin")), CallbackIdentity: run.ComputeDigest([]byte("ready sensor v1"))}
	program, err := planning.Compile[string](t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	output, err := rt.Run(t.Context(), program, "inspect")
	if err != nil || output.Outcome != planning.OutcomeAchieved {
		t.Fatalf("planning outcome=%v err=%v", output, err)
	}
	if closeErr := rt.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	cfg.CallbackIdentity = run.ComputeDigest([]byte("ready sensor v2"))
	changed, err := planning.Compile[string](t.Context(), cfg)
	if err != nil || changed.ProgramRef() == program.ProgramRef() {
		t.Fatalf("opaque bindings not pinned: %v", err)
	}
}

func TestPlanningCompilerRejectsUnboundExecutionAndPinsActionSemantics(t *testing.T) {
	ready, err := planning.NewCondition("ready", planning.TruthTrue)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := planning.NewGoal(planning.GoalConfig{Name: "ready", Description: "Make ready.", Conditions: []planning.Condition{ready}})
	if err != nil {
		t.Fatal(err)
	}
	action := func(name string) planning.ActionBinding {
		a, e := planning.NewAction(planning.ActionConfig{Name: name, Description: "Acquire the material.", Effects: []planning.Condition{ready}})
		if e != nil {
			t.Fatal(e)
		}
		b, e := planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: a})
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	executor := planning.ActionExecutorFunc(func(context.Context, planning.ActionRequest) (planning.ActionResult, error) {
		t.Fatal("compiler dispatched the action")
		return planning.ActionResult{}, nil
	})
	c := planning.Config{Name: "tests.ready", Description: "Produce readiness.", Goal: goal, Actions: []planning.ActionBinding{action("acquire")}, ActionExecutors: map[string]planning.ActionExecutor{"acquire": executor}, Planner: planning.PlannerFunc(func(context.Context, planning.Problem) (planning.Plan, bool, error) {
		t.Fatal("compiler executed planner")
		return planning.Plan{}, false, nil
	}), Sensor: planning.SensorFunc(func(context.Context, planning.SenseRequest) (planning.WorldState, error) {
		t.Fatal("compiler performed sensing")
		return planning.WorldState{}, nil
	}), BuildIdentity: run.ComputeDigest([]byte("build")), CallbackIdentity: run.ComputeDigest([]byte("bindings"))}
	p, err := planning.Compile[string](t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if q, e := planning.Compile[string](ctx, c); q.Valid() || !errors.Is(e, context.Canceled) {
		t.Fatal(q, e)
	}
	if q, e := planning.Compile[chan int](t.Context(), c); q.Valid() || e == nil {
		t.Fatal("unrepresentable planning input accepted")
	}
	noCallback := c
	noCallback.CallbackIdentity = run.Digest{}
	if q, e := planning.Compile[string](t.Context(), noCallback); q.Valid() || !errors.Is(e, run.ErrInvalidProgram) {
		t.Fatal(q, e)
	}
	badGoal := c
	badGoal.Goal = planning.Goal{}
	if q, e := planning.Compile[string](t.Context(), badGoal); q.Valid() || e == nil {
		t.Fatal("missing goal accepted")
	}
	c.Actions = []planning.ActionBinding{action("purchase")}
	if q, e := planning.Compile[string](t.Context(), c); q.Valid() || e == nil {
		t.Fatal("free-floating executor accepted")
	}
	c.ActionExecutors = map[string]planning.ActionExecutor{"purchase": executor}
	q, e := planning.Compile[string](t.Context(), c)
	if e != nil || p.ProgramRef() == q.ProgramRef() {
		t.Fatal("renaming an executable action did not alter the program", e)
	}
}
