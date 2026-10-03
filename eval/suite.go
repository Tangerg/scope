package eval

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"golang.org/x/sync/errgroup"
)

// DefaultSuiteConcurrency keeps nested experiment fan-out sequential unless
// the Host explicitly budgets concurrent assessments within each case.
const DefaultSuiteConcurrency = 1

// SuiteConfig declares independently identifiable assessments. MaxConcurrency
// limits direct assessment calls within one Run; zero selects one. ErrorCollect
// preserves failures without canceling independent assessments.
type SuiteConfig[T any] struct {
	Assessments    []Assessment[T]
	MaxConcurrency int
	ErrorPolicy    ErrorPolicy
}

// Suite executes independent assessments and preserves their individual
// execution outcomes. It is a collection operation, not an atomic Evaluator.
type Suite[T any] struct {
	assessments    []Assessment[T]
	maxConcurrency int
	errorPolicy    ErrorPolicy
}

func NewSuite[T any](config SuiteConfig[T]) (*Suite[T], error) {
	if len(config.Assessments) == 0 {
		return nil, fmt.Errorf("%w: at least one assessment is required", ErrInvalidEvaluatorConfig)
	}
	if config.MaxConcurrency < 0 {
		return nil, fmt.Errorf("%w: maximum concurrency must not be negative", ErrInvalidEvaluatorConfig)
	}
	seen := make(map[AssessmentID]struct{}, len(config.Assessments))
	for _, assessment := range config.Assessments {
		if err := assessment.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidEvaluatorConfig, err)
		}
		if _, exists := seen[assessment.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate assessment %q", ErrInvalidEvaluatorConfig, assessment.ID)
		}
		seen[assessment.ID] = struct{}{}
	}
	policy, err := config.ErrorPolicy.normalize()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEvaluatorConfig, err)
	}
	concurrency := config.MaxConcurrency
	if concurrency == 0 {
		concurrency = DefaultSuiteConcurrency
	}
	return &Suite[T]{
		assessments:    slices.Clone(config.Assessments),
		maxConcurrency: min(concurrency, len(config.Assessments)), errorPolicy: policy,
	}, nil
}

// Run always returns the results accumulated before failure or cancellation.
// Under ErrorCollect, individual errors live in Results and do not become the
// operation error. The caller's cancellation and fail-fast error remain visible.
func (s *Suite[T]) Run(ctx context.Context, subject T) (SuiteResult, error) {
	if s == nil || len(s.assessments) == 0 {
		return SuiteResult{}, fmt.Errorf("%w: uninitialized suite", ErrInvalidEvaluatorConfig)
	}
	result := s.unevaluated(nil)
	group := new(errgroup.Group)
	groupContext := ctx
	if s.errorPolicy == ErrorFailFast {
		group, groupContext = errgroup.WithContext(ctx)
	}
	group.SetLimit(s.maxConcurrency)
	for index, assessment := range s.assessments {
		if groupContext.Err() != nil {
			break
		}
		group.Go(func() error {
			result.Results[index] = assessment.run(groupContext, subject)
			if s.errorPolicy == ErrorFailFast && result.Results[index].Err != nil {
				return fmt.Errorf("eval: assessment %q: %w", assessment.ID, result.Results[index].Err)
			}
			return nil
		})
	}
	runErr := group.Wait()
	if err := ctx.Err(); err != nil {
		for index := range result.Results {
			if result.Results[index].Status() == AssessmentNotEvaluated {
				result.Results[index].Err = errors.Join(ErrNotEvaluated, err)
			}
		}
		return result, err
	}
	return result, runErr
}

func (s *Suite[T]) unevaluated(err error) SuiteResult {
	result := SuiteResult{Results: make([]AssessmentResult, len(s.assessments))}
	for index, assessment := range s.assessments {
		result.Results[index] = AssessmentResult{ID: assessment.ID, Err: errors.Join(ErrNotEvaluated, err)}
	}
	return result
}

// SuiteResult is an ordered set of assessment facts. Verdict and completion
// are derived, so there is no independently mutable aggregate outcome.
type SuiteResult struct {
	Results []AssessmentResult
}

func (s SuiteResult) Validate() error {
	if len(s.Results) == 0 {
		return fmt.Errorf("%w: suite result contains no assessments", ErrInvalidAssessment)
	}
	seen := make(map[AssessmentID]struct{}, len(s.Results))
	for _, result := range s.Results {
		if err := result.Validate(); err != nil {
			return err
		}
		if _, exists := seen[result.ID]; exists {
			return fmt.Errorf("%w: duplicate assessment result %q", ErrInvalidAssessment, result.ID)
		}
		seen[result.ID] = struct{}{}
	}
	return nil
}

func (s SuiteResult) Clone() (SuiteResult, error) {
	if err := s.Validate(); err != nil {
		return SuiteResult{}, err
	}
	return s.cloneValid(), nil
}

func (s SuiteResult) cloneValid() SuiteResult {
	s.Results = slices.Clone(s.Results)
	for index := range s.Results {
		s.Results[index] = s.Results[index].clone()
	}
	return s
}

func (s SuiteResult) Complete() bool {
	if len(s.Results) == 0 {
		return false
	}
	for _, result := range s.Results {
		if result.Status() != AssessmentCompleted {
			return false
		}
	}
	return true
}

func (s SuiteResult) Err() error {
	var failures []error
	for _, result := range s.Results {
		if result.Err != nil {
			failures = append(failures, fmt.Errorf("eval: assessment %q: %w", result.ID, result.Err))
		}
	}
	return errors.Join(failures...)
}

// Verdict is unspecified until every assessment completes. Successful reports
// remain individually available even when the suite has no complete verdict.
func (s SuiteResult) Verdict() Verdict {
	if !s.Complete() {
		return VerdictUnspecified
	}
	verdict := VerdictUnspecified
	for _, result := range s.Results {
		switch result.Report.Verdict() {
		case VerdictFail:
			return VerdictFail
		case VerdictPass:
			verdict = VerdictPass
		}
	}
	return verdict
}
