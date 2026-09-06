package eval

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

func evaluateAll[T any](ctx context.Context, evaluators []Evaluator[T], maxConcurrency int, subject T) ([]Report, error) {
	reports := make([]Report, len(evaluators))
	if len(evaluators) == 0 {
		return reports, nil
	}
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(min(max(1, maxConcurrency), len(evaluators)))
	for index, evaluator := range evaluators {
		group.Go(func() error {
			report, err := evaluator.Evaluate(groupContext, subject)
			if err != nil {
				return fmt.Errorf("eval: evaluator %d: %w", index, err)
			}
			if err := report.Validate(); err != nil {
				return fmt.Errorf("eval: evaluator %d: %w", index, err)
			}
			reports[index] = report
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return reports, nil
}
