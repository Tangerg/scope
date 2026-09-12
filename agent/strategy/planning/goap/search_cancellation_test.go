package goap

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/strategy/planning"
)

func TestGoalProducerScanObservesCancellationInsideEffects(t *testing.T) {
	var effects []planning.Condition
	for index := range 32 {
		effect, err := planning.NewCondition(fmt.Sprintf("fact.%d", index), planning.True)
		if err != nil {
			t.Fatal(err)
		}
		effects = append(effects, effect)
	}
	action, err := planning.NewAction(planning.ActionConfig{Name: "action.produce", Description: "Scan many effects.", Effects: effects})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := planning.NewGoal(planning.GoalConfig{Name: "goal.final", Description: "Find the final effect.", Conditions: effects[31:]})
	if err != nil {
		t.Fatal(err)
	}
	problem, err := planning.NewProblem(planning.WorldState{}, goal, action)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, planErr := New(Config{}).Plan(cancellationAfterChecks(t, 8), problem); !errors.Is(planErr, context.Canceled) || found {
		t.Fatalf("Planner lost preflight cancellation: found=%t error=%v", found, planErr)
	}
	search := newSearch(problem, 1, 1)
	if produced, err := search.hasGoalProducers(cancellationAfterChecks(t, 8)); !errors.Is(err, context.Canceled) || produced {
		t.Fatalf("producer scan ignored cancellation: produced=%t error=%v", produced, err)
	}
}

func TestReconstructionStopsWithoutReturningPartialPlan(t *testing.T) {
	search := &search{startKey: "0", predecessors: make(map[string]predecessor)}
	action, err := planning.NewPlannedAction("action.advance")
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 32; index++ {
		search.predecessors[fmt.Sprint(index)] = predecessor{stateKey: fmt.Sprint(index - 1), action: action}
	}
	if actions, err := search.reconstruct(cancellationAfterChecks(t, 8), "32"); !errors.Is(err, context.Canceled) || actions != nil {
		t.Fatalf("reconstruction returned a partial plan: %v, %v", actions, err)
	}
}

type scanCancellationContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining atomic.Int32
}

func cancellationAfterChecks(t *testing.T, checks int32) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := &scanCancellationContext{Context: ctx, cancel: cancel}
	result.remaining.Store(checks)
	return result
}

func (s *scanCancellationContext) Err() error {
	if s.remaining.Add(-1) <= 0 {
		s.cancel()
	}
	return s.Context.Err()
}
