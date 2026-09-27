package eval

import "context"

// Target executes an input and freezes the resulting candidate before grading.
// It receives only the public input; references, hidden tests, and evaluation
// policy belong to the assessment closure or a separate evaluation context.
//
// A non-nil error means no usable Execution was returned. Known target failure,
// cancellation, or budget exhaustion is a valid Execution with a stop reason,
// optionally retaining a candidate. Implementations must honor ctx and finish
// collecting their execution evidence before returning. They must not expose
// credentials, cleanup handles, or mutable workspaces as candidate artifacts.
type Target[I, O any] interface {
	Run(ctx context.Context, input I) (Execution[O], error)
}

// TargetFunc implements the same execution boundary for a Host-owned solver.
type TargetFunc[I, O any] func(context.Context, I) (Execution[O], error)

func (t TargetFunc[I, O]) Run(ctx context.Context, input I) (Execution[O], error) {
	return t(ctx, input)
}
