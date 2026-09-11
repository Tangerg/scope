package goap

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent/strategy/planning"
)

const defaultMaxExpansions uint32 = 10_000

// ErrExpansionLimitReached distinguishes bounded search from an unsatisfiable goal.
var ErrExpansionLimitReached = errors.New("goap: expansion limit reached")

// Config contains the bounded search policy for a GOAP Planner.
type Config struct {
	// MaxExpansions bounds states removed from the frontier. Zero selects a safe
	// default of 10,000 expansions.
	MaxExpansions uint32
}

// Planner performs stateless uniform-cost search and is safe for concurrent
// use after construction.
type Planner struct {
	maxExpansions uint32
}

// New returns a planner whose search is bounded by configuration, because
// GOAP's state space grows with the action set and an unbounded search inside
// a Step would block the Process that owns it.
func New(config Config) *Planner {
	limit := config.MaxExpansions
	if limit == 0 {
		limit = defaultMaxExpansions
	}
	return &Planner{maxExpansions: limit}
}

func (p *Planner) Plan(ctx context.Context, problem planning.Problem) (planning.Plan, bool, error) {
	if p == nil || p.maxExpansions == 0 || !problem.Valid() {
		return planning.Plan{}, false, planning.ErrInvalidProblem
	}
	if err := ctx.Err(); err != nil {
		return planning.Plan{}, false, err
	}
	if problem.Goal().SatisfiedBy(problem.InitialState()) {
		plan, err := planning.NewPlan(nil, 0)
		return plan, true, err
	}
	search := newSearch(problem, p.maxExpansions)
	producers, err := search.hasGoalProducers(ctx)
	if err != nil {
		return planning.Plan{}, false, err
	}
	if !producers {
		return planning.Plan{}, false, nil
	}
	goal, found, err := search.run(ctx)
	if err != nil {
		return planning.Plan{}, false, err
	}
	if !found {
		return planning.Plan{}, false, nil
	}
	actions, err := search.reconstruct(ctx, goal.state.Key())
	if err != nil {
		return planning.Plan{}, false, err
	}
	plan, err := planning.NewPlan(actions, goal.cost)
	if err != nil {
		return planning.Plan{}, false, err
	}
	if err := problem.ValidatePlan(plan); err != nil {
		return planning.Plan{}, false, fmt.Errorf("goap: validate result: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return planning.Plan{}, false, err
	}
	return plan, true, nil
}

var _ planning.Planner = (*Planner)(nil)
