package eval

import (
	"context"
	"fmt"

	"github.com/samber/lo"
)

// Projection narrows an aggregate case to the subject owned by one evaluator.
type Projection[T, Subject any] func(T) (Subject, error)

type ProjectionEvaluator[T, Subject any] struct {
	evaluator  Evaluator[Subject]
	projection Projection[T, Subject]
}

func NewProjectionEvaluator[T, Subject any](
	evaluator Evaluator[Subject],
	projection Projection[T, Subject],
) (*ProjectionEvaluator[T, Subject], error) {
	if lo.IsNil(evaluator) {
		return nil, fmt.Errorf("%w: evaluator is nil", ErrInvalidEvaluatorConfig)
	}
	if projection == nil {
		return nil, fmt.Errorf("%w: projection is nil", ErrInvalidEvaluatorConfig)
	}
	return &ProjectionEvaluator[T, Subject]{evaluator: evaluator, projection: projection}, nil
}

func (p *ProjectionEvaluator[T, Subject]) Evaluate(ctx context.Context, value T) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	subject, err := p.projection(value)
	if err != nil {
		return Report{}, fmt.Errorf("eval: project subject: %w", err)
	}
	return p.evaluator.Evaluate(ctx, subject)
}
