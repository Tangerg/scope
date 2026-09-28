package planning

import "context"

// Planner searches deterministically without side effects and is safe for concurrent use.
type Planner interface {
	// Plan searches one immutable Problem without mutating it or performing I/O.
	// found=false with nil error is reserved for an exhausted complete search;
	// cancellation, resource limits, invalid costs, and internal failure return
	// errors. Equivalent Problems must produce an equivalent ordered Plan.
	Plan(ctx context.Context, problem Problem) (plan Plan, found bool, err error)
}

type PlannerFunc func(ctx context.Context, problem Problem) (plan Plan, found bool, err error)

func (p PlannerFunc) Plan(
	ctx context.Context,
	problem Problem,
) (Plan, bool, error) {
	return p(ctx, problem)
}
