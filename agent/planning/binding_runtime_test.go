package planning_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/planning"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestCompiledActionBindingsKeepTheirExecutorAcrossChildSlots(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	done, goal, _ := domain()
	ready := require(planning.NewCondition("world.ready", planning.TruthTrue))
	unused := require(planning.NewCondition("world.unused", planning.TruthTrue))
	prepare := require(planning.NewAction(planning.ActionConfig{Name: "prepare", Description: "Prepare the work.", Effects: []planning.Condition{ready}}))
	finish := require(planning.NewAction(planning.ActionConfig{Name: "finish", Description: "Finish prepared work.", Preconditions: []planning.Condition{ready}, Effects: []planning.Condition{done}}))
	childAction := require(planning.NewAction(planning.ActionConfig{Name: "unused", Description: "Run an unrelated child.", Effects: []planning.Condition{unused}}))
	childStage := require(workflow.Transform("identity", func(_ context.Context, value string) (string, error) { return value, nil }))
	childProgram := require(workflow.Compile[string, string](runtime.Compiler(), workflow.Config{
		Name: "test.planning.unused-child", Description: "Keep a child action between dispatcher actions.", Revision: "identity", Stages: []workflow.Stage{childStage},
	}))
	world := &observedWorld{}
	var preparations, finishes atomic.Int32
	executor := func(action planning.Action, effect planning.Condition, count *atomic.Int32) planning.ActionExecutorFunc {
		return func(_ context.Context, request planning.ActionRequest) (planning.ActionResult, error) {
			if request.ActionName != action.Name() || request.ActionDescription != action.Description() {
				t.Errorf("executor received another action's declaration: %+v", request)
			}
			count.Add(1)
			world.apply(effect)
			return planning.ActionSucceeded(), nil
		}
	}
	config := baseConfig(goal, world,
		require(planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: prepare, Executor: executor(prepare, ready, &preparations)})),
		require(planning.NewChildBinding(planning.ChildBindingConfig{Action: childAction, Child: run.ChildBinding{Program: childProgram.Erase()}})),
		require(planning.NewDispatcherBinding(planning.DispatcherBindingConfig{Action: finish, Executor: executor(finish, done, &finishes)})),
	)
	program := require(planning.Compile[string](runtime.Compiler(), config))
	config.Actions[0], config.Actions[2] = config.Actions[2], config.Actions[0]
	output := require(runtime.Run(t.Context(), program, "work"))
	if output.Outcome != planning.OutcomeAchieved || len(output.Attempts) != 2 || preparations.Load() != 1 || finishes.Load() != 1 {
		t.Fatalf("compiled bindings changed their execution owner: output=%+v prepare=%d finish=%d", output, preparations.Load(), finishes.Load())
	}
	if string(output.Attempts[0].ActionName) != prepare.Name() || string(output.Attempts[1].ActionName) != finish.Name() {
		t.Fatalf("execution order disagrees with the frozen declarations: %+v", output.Attempts)
	}
}
