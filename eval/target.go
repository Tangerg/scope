package eval

import "context"

// Target executes the public input and freezes its candidate before grading;
// references and hidden tests stay with the assessments. A non-nil error means
// no usable Execution; known failure, cancellation, or budget exhaustion is an
// Execution with a stop reason. Implementations honor ctx, finish collecting
// evidence before returning, and never expose credentials, cleanup handles, or
// mutable workspaces as candidates.
type Target[I, O any] interface {
	Run(ctx context.Context, input I) (Execution[O], error)
}

type TargetFunc[I, O any] func(context.Context, I) (Execution[O], error)

func (t TargetFunc[I, O]) Run(ctx context.Context, input I) (Execution[O], error) {
	return t(ctx, input)
}
