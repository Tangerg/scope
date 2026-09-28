package eval

import "context"

type Evaluator[T any] interface {
	// Evaluate inspects one subject without mutating it and returns a valid,
	// owned report for the evaluator's metric. Implementations must honor ctx;
	// a non-nil error means the report must not be consumed.
	Evaluate(ctx context.Context, subject T) (Report, error)
}

type EvaluatorFunc[T any] func(context.Context, T) (Report, error)

func (e EvaluatorFunc[T]) Evaluate(ctx context.Context, subject T) (Report, error) {
	return e(ctx, subject)
}
