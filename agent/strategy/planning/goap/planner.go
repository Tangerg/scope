package goap

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/planning"
)

// ErrExpansionLimitReached distinguishes bounded search from an unsatisfiable goal.
var ErrExpansionLimitReached = errors.New("goap: expansion limit reached")

// ErrGenerationLimitReached means the search exhausted its node budget.
var ErrGenerationLimitReached = errors.New("goap: generated node limit reached")

// Config contains optional cumulative search quotas for a GOAP Planner.
type Config struct {
	// MaxExpansions bounds non-stale nodes removed from the frontier.
	// Its zero value is unlimited. A finite zero permits an already satisfied
	// Goal but rejects search with ErrExpansionLimitReached.
	MaxExpansions agent.Quota

	// MaxGeneratedNodes bounds cumulative frontier insertions, including the
	// initial node and cheaper replacements of discovered states. Its zero value
	// is unlimited; a finite zero rejects search with ErrGenerationLimitReached.
	// Already satisfied Goals need no search. This bounds entries, not byte size.
	MaxGeneratedNodes agent.Quota
}

// Planner performs stateless uniform-cost search and is safe for concurrent
// use after construction.
type Planner struct {
	maxExpansions     agent.Quota
	maxGeneratedNodes agent.Quota
}

// New returns a planner with Host-selected search quotas. Unlimited searches
// still cooperate with context cancellation.
func New(config Config) *Planner {
	return &Planner{maxExpansions: config.MaxExpansions, maxGeneratedNodes: config.MaxGeneratedNodes}
}

func (p *Planner) Plan(ctx context.Context, problem planning.Problem) (planning.Plan, bool, error) {
	if p == nil || !problem.Valid() {
		return planning.Plan{}, false, planning.ErrInvalidProblem
	}
	if err := ctx.Err(); err != nil {
		return planning.Plan{}, false, err
	}
	if problem.Goal().SatisfiedBy(problem.InitialState()) {
		plan, err := planning.NewPlan(nil, 0)
		return plan, true, err
	}
	if !p.maxGeneratedNodes.Allows(1) {
		return planning.Plan{}, false, ErrGenerationLimitReached
	}
	search := newSearch(problem, p.maxExpansions, p.maxGeneratedNodes)
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
