package planning

import (
	"context"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func BenchmarkActionHistory(b *testing.B) {
	for _, count := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			fact, err := NewCondition("world.done", True)
			if err != nil {
				b.Fatal(err)
			}
			goal, err := NewGoal(GoalConfig{Name: "goal.done", Description: "Finish work.", Conditions: []Condition{fact}})
			if err != nil {
				b.Fatal(err)
			}
			schema, err := agent.SchemaFor[string]()
			if err != nil {
				b.Fatal(err)
			}
			bindings := make([]ActionBinding, count)
			attempts := make([]Attempt, count)
			for index := range count {
				name := fmt.Sprintf("action.%04d", index)
				action, actionErr := NewAction(ActionConfig{Name: name, Description: "Finish work.", Effects: []Condition{fact}})
				if actionErr != nil {
					b.Fatal(actionErr)
				}
				bindings[index], err = NewDispatcherBinding(DispatcherBindingConfig{Action: action})
				if err != nil {
					b.Fatal(err)
				}
				attempts[index] = Attempt{ActionName: name, Status: AttemptFailed, Diagnostic: "operation failed"}
			}
			definition, err := NewDefinition(DefinitionConfig{Name: "benchmark.planning", Description: "Measure action history admission.", InputSchema: schema, Goal: goal, Actions: bindings, MaxActionAttempts: agent.NewQuota(uint64(count)), Planner: PlannerFunc(func(context.Context, Problem) (Plan, bool, error) { return Plan{}, false, nil })})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := definition.validateActionHistory(context.Background(), attempts); err != nil {
					b.Fatal(err)
				}
				problem, err := definition.problem(executionState{Attempts: attempts})
				if err != nil || len(problem.Actions()) != 0 {
					b.Fatalf("problem = %+v, %v", problem, err)
				}
			}
		})
	}
}
