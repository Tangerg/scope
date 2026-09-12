package goap_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/planning"
	"github.com/Tangerg/scope/agent/strategy/planning/goap"
)

func TestPlannerFindsCheapestMultiRoutePlan(t *testing.T) {
	resource := condition(t, "world.resource", planning.True)
	done := condition(t, "world.done", planning.True)
	problem := mustProblem(t, planning.WorldState{}, goal(t, done),
		action(t, "action.buy", nil, []planning.Condition{resource}, 4),
		action(t, "action.forage", nil, []planning.Condition{resource}, 1),
		action(t, "action.build", []planning.Condition{resource}, []planning.Condition{done}, 1),
		action(t, "action.direct", nil, []planning.Condition{done}, 5),
	)

	plan, found, err := goap.New(goap.Config{}).Plan(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	if !found || plan.TotalCost() != 2 || !slices.Equal(actionNames(plan), []string{"action.forage", "action.build"}) {
		t.Fatalf("plan = %v cost=%v found=%t", actionNames(plan), plan.TotalCost(), found)
	}
}

func TestPlannerEvaluatesDynamicCostAtTransitionSource(t *testing.T) {
	unlocked := condition(t, "world.unlocked", planning.True)
	done := condition(t, "world.done", planning.True)
	dynamic, err := planning.NewAction(planning.ActionConfig{
		Name: "action.fast", Description: "Finish cheaply after unlocking.",
		Effects: []planning.Condition{done},
		Cost: func(state planning.WorldState) (float64, error) {
			if state.Truth("world.unlocked") == planning.True {
				return 1, nil
			}
			return 100, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	problem := mustProblem(t, planning.WorldState{}, goal(t, done),
		action(t, "action.unlock", nil, []planning.Condition{unlocked}, 1),
		dynamic,
		action(t, "action.direct", nil, []planning.Condition{done}, 5),
	)
	plan, found, err := goap.New(goap.Config{}).Plan(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	if !found || plan.TotalCost() != 2 || !slices.Equal(actionNames(plan), []string{"action.unlock", "action.fast"}) {
		t.Fatalf("plan = %v cost=%v found=%t", actionNames(plan), plan.TotalCost(), found)
	}
}

func TestPlannerReplacesAStatePredecessorOnlyWithCheaperPath(t *testing.T) {
	prepared := condition(t, "world.prepared", planning.True)
	resource := condition(t, "world.resource", planning.True)
	done := condition(t, "world.done", planning.True)
	problem := mustProblem(t, planning.WorldState{}, goal(t, done),
		action(t, "action.expensive_resource", nil, []planning.Condition{prepared, resource}, 9),
		action(t, "action.prepare", nil, []planning.Condition{prepared}, 1),
		action(t, "action.cheap_resource", []planning.Condition{prepared}, []planning.Condition{resource}, 1),
		action(t, "action.finish", []planning.Condition{resource}, []planning.Condition{done}, 1),
	)

	plan, found, err := goap.New(goap.Config{}).Plan(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"action.prepare", "action.cheap_resource", "action.finish"}
	if !found || plan.TotalCost() != 3 || !slices.Equal(actionNames(plan), want) {
		t.Fatalf("plan = %v cost=%v found=%t", actionNames(plan), plan.TotalCost(), found)
	}
}

func TestPlannerPreservesDeclarationOrderForEqualCostRoutes(t *testing.T) {
	done := condition(t, "world.done", planning.True)
	problem := mustProblem(t, planning.WorldState{}, goal(t, done),
		action(t, "action.first", nil, []planning.Condition{done}, 1),
		action(t, "action.second", nil, []planning.Condition{done}, 1),
	)
	plan, found, err := goap.New(goap.Config{}).Plan(t.Context(), problem)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !slices.Equal(actionNames(plan), []string{"action.first"}) {
		t.Fatalf("plan = %v found=%t", actionNames(plan), found)
	}
}

func TestPlannerDistinguishesSatisfiedUnreachableAndBoundedSearch(t *testing.T) {
	done := condition(t, "world.done", planning.True)
	key := condition(t, "world.key", planning.True)
	t.Run("already satisfied", func(t *testing.T) {
		problem := mustProblem(t, world(t, done), goal(t, done))
		plan, found, err := goap.New(goap.Config{}).Plan(t.Context(), problem)
		if err != nil || !found || len(plan.Actions()) != 0 || plan.TotalCost() != 0 {
			t.Fatalf("plan=%v found=%t error=%v", plan, found, err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		problem := mustProblem(t, planning.WorldState{}, goal(t, done),
			action(t, "action.finish", []planning.Condition{key}, []planning.Condition{done}, 1),
		)
		_, found, err := goap.New(goap.Config{}).Plan(t.Context(), problem)
		if err != nil || found {
			t.Fatalf("found=%t error=%v", found, err)
		}
	})
	t.Run("expansion limit", func(t *testing.T) {
		ready := condition(t, "world.ready", planning.True)
		problem := mustProblem(t, planning.WorldState{}, goal(t, done),
			action(t, "action.prepare", nil, []planning.Condition{ready}, 1),
			action(t, "action.finish", []planning.Condition{ready}, []planning.Condition{done}, 1),
		)
		_, found, err := goap.New(goap.Config{MaxExpansions: 1}).Plan(t.Context(), problem)
		if found || !errors.Is(err, goap.ErrExpansionLimitReached) {
			t.Fatalf("found=%t error=%v", found, err)
		}
	})
}

func TestPlannerRejectsInvalidActionCosts(t *testing.T) {
	done := condition(t, "world.done", planning.True)
	cause := errors.New("cost sentinel")
	tests := []struct {
		name string
		cost planning.CostFunc
		want error
	}{
		{name: "negative", cost: planning.FixedCost(-1), want: planning.ErrInvalidActionCost},
		{name: "nan", cost: planning.FixedCost(math.NaN()), want: planning.ErrInvalidActionCost},
		{name: "infinite", cost: planning.FixedCost(math.Inf(1)), want: planning.ErrInvalidActionCost},
		{name: "error", cost: func(planning.WorldState) (float64, error) { return 0, cause }, want: cause},
		{name: "panic", cost: func(planning.WorldState) (float64, error) { panic(cause) }, want: cause},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid, err := planning.NewAction(planning.ActionConfig{
				Name: "action.invalid", Description: "Expose an invalid cost.",
				Effects: []planning.Condition{done}, Cost: test.cost,
			})
			if err != nil {
				t.Fatal(err)
			}
			problem := mustProblem(t, planning.WorldState{}, goal(t, done), invalid)
			_, found, err := goap.New(goap.Config{}).Plan(t.Context(), problem)
			if found || !errors.Is(err, planning.ErrInvalidActionCost) || !errors.Is(err, test.want) {
				t.Fatalf("found=%t error=%v", found, err)
			}
		})
	}
}

func TestPlannerHonorsCancellation(t *testing.T) {
	done := condition(t, "world.done", planning.True)
	problem := mustProblem(t, planning.WorldState{}, goal(t, done),
		action(t, "action.finish", nil, []planning.Condition{done}, 1),
	)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, found, err := goap.New(goap.Config{}).Plan(ctx, problem)
	if found || !errors.Is(err, context.Canceled) {
		t.Fatalf("found=%t error=%v", found, err)
	}
}

func TestPlannerBoundsGeneratedNodesIndependentlyOfExpansions(t *testing.T) {
	done := condition(t, "world.done", planning.True)
	var actions []planning.Action
	for index := range 32 {
		fact := condition(t, fmt.Sprintf("world.branch_%d", index), planning.True)
		actions = append(actions, action(t, fmt.Sprintf("action.branch_%d", index), nil, []planning.Condition{fact}, 1))
	}
	actions = append(actions, action(t, "action.finish", nil, []planning.Condition{done}, 100))
	problem := mustProblem(t, planning.WorldState{}, goal(t, done), actions...)
	_, found, err := goap.New(goap.Config{MaxExpansions: 1, MaxGeneratedNodes: 8}).Plan(t.Context(), problem)
	if found || !errors.Is(err, goap.ErrGenerationLimitReached) || errors.Is(err, goap.ErrExpansionLimitReached) {
		t.Fatalf("found=%t error=%v", found, err)
	}
}

func TestPlannerCountsCheaperReplacementNodes(t *testing.T) {
	done := condition(t, "world.done", planning.True)
	problem := mustProblem(t, planning.WorldState{}, goal(t, done),
		action(t, "action.expensive", nil, []planning.Condition{done}, 3),
		action(t, "action.cheaper", nil, []planning.Condition{done}, 2),
		action(t, "action.cheapest", nil, []planning.Condition{done}, 1),
	)
	for _, limit := range []uint32{1, 2, 3, 4} {
		plan, found, err := goap.New(goap.Config{MaxGeneratedNodes: limit}).Plan(t.Context(), problem)
		if limit < 4 {
			if found || !errors.Is(err, goap.ErrGenerationLimitReached) {
				t.Fatalf("limit=%d found=%t error=%v", limit, found, err)
			}
		} else if err != nil || !found || plan.TotalCost() != 1 || !slices.Equal(actionNames(plan), []string{"action.cheapest"}) {
			t.Fatalf("limit=%d plan=%v found=%t error=%v", limit, plan, found, err)
		}
	}
}

func TestPlannerNodeBudgetPreservesSatisfiedAndUnreachableResults(t *testing.T) {
	done := condition(t, "world.done", planning.True)
	key := condition(t, "world.key", planning.True)
	planner := goap.New(goap.Config{MaxGeneratedNodes: 1})
	satisfied := mustProblem(t, world(t, done), goal(t, done))
	if plan, found, err := planner.Plan(t.Context(), satisfied); err != nil || !found || len(plan.Actions()) != 0 {
		t.Fatalf("satisfied plan=%v found=%t error=%v", plan, found, err)
	}
	unreachable := mustProblem(t, planning.WorldState{}, goal(t, done),
		action(t, "action.blocked", []planning.Condition{key}, []planning.Condition{done}, 1))
	if _, found, err := planner.Plan(t.Context(), unreachable); err != nil || found {
		t.Fatalf("unreachable found=%t error=%v", found, err)
	}
}

func condition(t *testing.T, key string, truth planning.Truth) planning.Condition {
	t.Helper()
	value, err := planning.NewCondition(key, truth)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func world(t *testing.T, conditions ...planning.Condition) planning.WorldState {
	t.Helper()
	value, err := planning.NewWorldState(conditions...)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func goal(t *testing.T, conditions ...planning.Condition) planning.Goal {
	t.Helper()
	value, err := planning.NewGoal(planning.GoalConfig{
		Name: "goal.test", Description: "Reach the test goal.", Conditions: conditions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func action(
	t *testing.T,
	name string,
	preconditions []planning.Condition,
	effects []planning.Condition,
	cost float64,
) planning.Action {
	t.Helper()
	value, err := planning.NewAction(planning.ActionConfig{
		Name: name, Description: "Apply " + name + ".",
		Preconditions: preconditions, Effects: effects, Cost: planning.FixedCost(cost),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustProblem(
	t *testing.T,
	initial planning.WorldState,
	goal planning.Goal,
	actions ...planning.Action,
) planning.Problem {
	t.Helper()
	problem, err := planning.NewProblem(initial, goal, actions...)
	if err != nil {
		t.Fatal(err)
	}
	return problem
}

func actionNames(plan planning.Plan) []string {
	actions := plan.Actions()
	names := make([]string, len(actions))
	for index, action := range actions {
		names[index] = action.Name()
	}
	return names
}

func TestPlannerStopsExpansionWhenCostCancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := condition(t, "world.done", planning.True)
	calls := 0
	var actions []planning.Action
	for _, name := range []string{"action.first", "action.second"} {
		candidate, err := planning.NewAction(planning.ActionConfig{
			Name: name, Description: "Cancel during one expansion.", Effects: []planning.Condition{done},
			Cost: func(planning.WorldState) (float64, error) { calls++; cancel(); return 1, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		actions = append(actions, candidate)
	}
	problem := mustProblem(t, planning.WorldState{}, goal(t, done), actions...)
	if _, found, err := goap.New(goap.Config{}).Plan(ctx, problem); found || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled expansion = found:%t error:%v", found, err)
	}
	if calls != 1 {
		t.Fatalf("cost callbacks after cancellation: %d", calls)
	}
}
