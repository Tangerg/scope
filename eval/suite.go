package eval

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/metadata"
)

// MetricNameSuite identifies a heterogeneous report that preserves child
// metrics instead of aggregating unlike scores.
const MetricNameSuite MetricName = "suite"

// SuiteConfig groups heterogeneous evaluators without collapsing their
// results into one score. A zero MaxConcurrency selects
// DefaultMaxConcurrency.
type SuiteConfig[T any] struct {
	Evaluators     []Evaluator[T]
	MaxConcurrency int
}

// SuiteEvaluator preserves every child report and records the ordered child
// metrics in its own identity. Its verdict fails when any decided child fails,
// passes when at least one child passes and none fail, and remains unspecified
// when every child is measurement-only or qualitative.
type SuiteEvaluator[T any] struct {
	evaluators     []Evaluator[T]
	maxConcurrency int
}

// NewSuiteEvaluator snapshots the ordered evaluator set and bounds concurrent
// child evaluation.
func NewSuiteEvaluator[T any](config SuiteConfig[T]) (*SuiteEvaluator[T], error) {
	if len(config.Evaluators) == 0 {
		return nil, fmt.Errorf("%w: at least one evaluator is required", ErrInvalidEvaluatorConfig)
	}
	if config.MaxConcurrency < 0 {
		return nil, fmt.Errorf("%w: maximum concurrency must not be negative", ErrInvalidEvaluatorConfig)
	}
	evaluators := make([]Evaluator[T], len(config.Evaluators))
	for index, evaluator := range config.Evaluators {
		if lo.IsNil(evaluator) {
			return nil, fmt.Errorf("%w: evaluators[%d] is nil", ErrInvalidEvaluatorConfig, index)
		}
		evaluators[index] = evaluator
	}
	maxConcurrency := config.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = DefaultMaxConcurrency
	}
	maxConcurrency = min(maxConcurrency, len(evaluators))
	return &SuiteEvaluator[T]{evaluators: evaluators, maxConcurrency: maxConcurrency}, nil
}

func (s *SuiteEvaluator[T]) Evaluate(ctx context.Context, subject T) (Report, error) {
	reports, err := evaluateAll(ctx, s.evaluators, s.maxConcurrency, subject)
	if err != nil {
		return Report{}, err
	}
	verdict := VerdictUnspecified
	for _, report := range reports {
		switch report.Verdict {
		case VerdictFail:
			verdict = VerdictFail
		case VerdictPass:
			if verdict == VerdictUnspecified {
				verdict = VerdictPass
			}
		}
	}
	metric, err := suiteMetric(reports)
	if err != nil {
		return Report{}, err
	}
	result := Report{Metric: metric, Verdict: verdict, Details: reports}
	if err := result.Validate(); err != nil {
		return Report{}, err
	}
	return result, nil
}

type suiteMetricIdentity struct {
	Metrics []Metric `json:"metrics"`
}

func suiteMetric(reports []Report) (Metric, error) {
	metrics := make([]Metric, len(reports))
	for index, report := range reports {
		metrics[index] = report.Metric
	}
	parameters := metadata.Map{}
	if err := parameters.Set(metricConfigurationKey, suiteMetricIdentity{Metrics: metrics}); err != nil {
		return Metric{}, fmt.Errorf("eval: suite metric identity: %w", err)
	}
	metric, err := NewMetric(MetricConfig{Name: MetricNameSuite, Parameters: parameters})
	if err != nil {
		return Metric{}, fmt.Errorf("eval: suite metric identity: %w", err)
	}
	return metric, nil
}

var _ Evaluator[struct{}] = (*SuiteEvaluator[struct{}])(nil)
