package goap_test

import (
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/planning"
	"github.com/Tangerg/scope/agent/strategy/planning/goap"
)

// Each state has one applicable successor, so growth measures framework work
// across a fixed route rather than an exponentially growing search space.
func BenchmarkPlannerChain(b *testing.B) {
	for _, count := range []int{8, 32, 128} {
		b.Run(fmt.Sprintf("actions_%d", count), func(b *testing.B) {
			problem := chainProblem(b, count)
			planner := goap.New(goap.Config{})
			b.ReportAllocs()
			for b.Loop() {
				plan, found, err := planner.Plan(b.Context(), problem)
				if err != nil || !found || len(plan.Actions()) != count || plan.TotalCost() != float64(count) {
					b.Fatalf("invalid chain result: found=%t cost=%v err=%v", found, plan.TotalCost(), err)
				}
			}
		})
	}
}

func chainProblem(b *testing.B, count int) planning.Problem {
	b.Helper()
	truths := make([]planning.Condition, count)
	facts := make([]planning.Condition, count)
	actions := make([]planning.Action, count)
	for index := range count {
		key := fmt.Sprintf("fact.%03d", index)
		var err error
		truths[index], err = planning.NewCondition(key, planning.True)
		if err != nil {
			b.Fatal(err)
		}
		facts[index], err = planning.NewCondition(key, planning.False)
		if err != nil {
			b.Fatal(err)
		}
		required := []planning.Condition{facts[index]}
		if index > 0 {
			required = append(required, truths[index-1])
		}
		actions[index], err = planning.NewAction(planning.ActionConfig{
			Name: fmt.Sprintf("action.%03d", index), Description: "Advance the chain.",
			Preconditions: required, Effects: []planning.Condition{truths[index]},
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	initial, err := planning.NewWorldState(facts...)
	if err != nil {
		b.Fatal(err)
	}
	goal, err := planning.NewGoal(planning.GoalConfig{
		Name: "goal.complete", Description: "Complete the chain.", Conditions: truths[count-1:],
	})
	if err != nil {
		b.Fatal(err)
	}
	problem, err := planning.NewProblem(initial, goal, actions...)
	if err != nil {
		b.Fatal(err)
	}
	return problem
}
