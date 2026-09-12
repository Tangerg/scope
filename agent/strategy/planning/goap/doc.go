// Package goap provides deterministic goal-oriented action planning over
// immutable planning.WorldState values. Its Planner uses uniform-cost search,
// which is complete and cost-optimal for the non-negative finite Action costs
// accepted by the Planning domain when the search completes within its expansion
// and generated-node budgets. Exhausting either budget returns an error distinct
// from a completed search that found no plan.
package goap
