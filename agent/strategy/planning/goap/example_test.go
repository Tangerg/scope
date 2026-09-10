package goap_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/strategy/planning"
	"github.com/Tangerg/scope/agent/strategy/planning/goap"
)

func ExamplePlanner_Plan() {
	ready, err := planning.NewCondition("service.ready", planning.True)
	if err != nil {
		panic(err)
	}
	state, err := planning.NewWorldState(ready)
	if err != nil {
		panic(err)
	}
	goal, err := planning.NewGoal(planning.GoalConfig{
		Name: "service.ready", Description: "The service is ready.", Conditions: []planning.Condition{ready},
	})
	if err != nil {
		panic(err)
	}
	problem, err := planning.NewProblem(state, goal)
	if err != nil {
		panic(err)
	}
	plan, found, err := goap.New(goap.Config{}).Plan(context.Background(), problem)
	if err != nil {
		panic(err)
	}

	fmt.Println(found, len(plan.Actions()), plan.TotalCost())
	// Output:
	// true 0 0
}
