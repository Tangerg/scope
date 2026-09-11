package planning_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/planning"
)

func BenchmarkPlanningReplayBoundary(b *testing.B) {
	condition, err := planning.NewCondition("world.done", planning.True)
	if err != nil {
		b.Fatal(err)
	}
	goal, err := planning.NewGoal(planning.GoalConfig{
		Name: "goal.done", Description: "Establish completion.", Conditions: []planning.Condition{condition},
	})
	if err != nil {
		b.Fatal(err)
	}
	schema, err := agent.SchemaFor[string]()
	if err != nil {
		b.Fatal(err)
	}
	definition, err := planning.NewDefinition(planning.DefinitionConfig{
		Name: "benchmark.planning", Description: "Measure Planning recovery admission.",
		InputSchema: schema, Goal: goal, MaxActionAttempts: 1,
		Planner: planning.PlannerFunc(func(context.Context, planning.Problem) (planning.Plan, bool, error) {
			return planning.Plan{}, false, nil
		}),
	})
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []int{1 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("input_bytes_%d", size), func(b *testing.B) {
			input, err := agent.EncodeInput(strings.Repeat("x", size))
			if err != nil {
				b.Fatal(err)
			}
			execution, err := definition.Start(input)
			if err != nil {
				b.Fatal(err)
			}
			initial, err := execution.Snapshot()
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				execution, err := definition.Restore(initial)
				if err != nil {
					b.Fatal(err)
				}
				transition, err := execution.Step(b.Context(), nil)
				if err != nil || !transition.Valid() {
					b.Fatalf("Step = %v, %v", transition, err)
				}
				state, err := execution.Snapshot()
				if err != nil {
					b.Fatal(err)
				}
				if _, err := definition.Restore(state); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
