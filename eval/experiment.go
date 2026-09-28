package eval

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

const DefaultMaxConcurrency = 4

// ErrorPolicy controls whether independent failures are collected or stop new
// scheduling at the Suite or Experiment boundary that owns the policy.
type ErrorPolicy string

// Error policies never hide failures from assessment results.
const (
	ErrorCollect  ErrorPolicy = "collect"
	ErrorFailFast ErrorPolicy = "fail_fast"
)

func (e ErrorPolicy) normalize() (ErrorPolicy, error) {
	if e == "" {
		return ErrorCollect, nil
	}
	switch e {
	case ErrorCollect, ErrorFailFast:
		return e, nil
	default:
		return "", fmt.Errorf("eval: unsupported error policy %q", e)
	}
}

// ExperimentConfig binds fixed case inputs and context to a Suite. MaxConcurrency
// limits concurrent cases; zero selects DefaultMaxConcurrency. A Suite defaults
// to one assessment at a time. Explicitly increasing both limits multiplies the
// maximum number of direct assessment calls.
type ExperimentConfig[T any] struct {
	Dataset        Dataset[T]
	Suite          *Suite[T]
	MaxConcurrency int
	ErrorPolicy    ErrorPolicy
}

// Experiment is an immutable plan for evaluating one Dataset. It owns bounded
// scheduling and error semantics, but no persistence, artifacts, or product
// identity.
type Experiment[T any] struct {
	dataset        Dataset[T]
	suite          *Suite[T]
	maxConcurrency int
	errorPolicy    ErrorPolicy
}

func NewExperiment[T any](config ExperimentConfig[T]) (Experiment[T], error) {
	if config.Dataset.fixtureID == "" {
		return Experiment[T]{}, fmt.Errorf("%w: dataset fixture identity is required", ErrInvalidExperiment)
	}
	if config.Suite == nil || len(config.Suite.assessments) == 0 {
		return Experiment[T]{}, fmt.Errorf("%w: suite is uninitialized", ErrInvalidExperiment)
	}
	if config.MaxConcurrency < 0 {
		return Experiment[T]{}, fmt.Errorf("%w: maximum concurrency must not be negative", ErrInvalidExperiment)
	}
	policy, err := config.ErrorPolicy.normalize()
	if err != nil {
		return Experiment[T]{}, fmt.Errorf("%w: %w", ErrInvalidExperiment, err)
	}
	maxConcurrency := config.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = DefaultMaxConcurrency
	}
	return Experiment[T]{
		dataset: config.Dataset, suite: config.Suite,
		maxConcurrency: maxConcurrency, errorPolicy: policy,
	}, nil
}

func (e Experiment[T]) Run(ctx context.Context) (ExperimentReport, error) {
	if e.suite == nil || e.dataset.fixtureID == "" {
		return ExperimentReport{}, fmt.Errorf("%w: uninitialized experiment", ErrInvalidExperiment)
	}
	cases := e.dataset.cases
	results := make([]CaseResult, len(cases))
	for index, caseValue := range cases {
		results[index] = CaseResult{ID: caseValue.ID, Metadata: caseValue.Metadata.Clone(), Result: e.suite.unevaluated(ErrNotEvaluated)}
	}
	if len(cases) == 0 {
		report, err := NewExperimentReport(e.dataset.fixtureID, results)
		if err != nil {
			return report, err
		}
		return report, ctx.Err()
	}

	attempted, runErr := e.execute(ctx, cases, results)
	if err := ctx.Err(); err != nil {
		for index := range results {
			if !attempted[index] {
				results[index].Result = e.suite.unevaluated(err)
			}
		}
	}
	report, summaryErr := NewExperimentReport(e.dataset.fixtureID, results)
	if runErr != nil {
		return report, runErr
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if summaryErr != nil {
		return report, summaryErr
	}
	return report, nil
}

func (e Experiment[T]) execute(
	ctx context.Context,
	cases []Case[T],
	results []CaseResult,
) ([]bool, error) {
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(min(e.maxConcurrency, len(cases)))
	attempted := make([]bool, len(cases))
	for index, caseValue := range cases {
		if groupContext.Err() != nil {
			break
		}
		group.Go(func() error {
			if groupContext.Err() != nil {
				return nil
			}
			attempted[index] = true
			result, err := e.suite.Run(groupContext, caseValue.Subject)
			results[index].Result = result
			if e.errorPolicy == ErrorFailFast {
				if err == nil {
					err = result.Err()
				}
				if err != nil {
					return fmt.Errorf("eval: case %q: %w", caseValue.ID, err)
				}
			}
			return nil
		})
	}
	return attempted, group.Wait()
}
