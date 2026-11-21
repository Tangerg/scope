package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/samber/lo"
)

// AssessmentID identifies an evaluation independently of whether it succeeds.
// The Host keeps this identity stable when comparing the same assessment.
type AssessmentID string

func (a AssessmentID) Validate() error {
	if !utf8.ValidString(string(a)) {
		return fmt.Errorf("%w: assessment id must be valid UTF-8", ErrInvalidAssessment)
	}
	if a == "" || strings.TrimSpace(string(a)) != string(a) {
		return fmt.Errorf("%w: assessment id must be non-empty without surrounding whitespace", ErrInvalidAssessment)
	}
	return nil
}

type AssessmentConfig[T any] struct {
	ID        AssessmentID
	Evaluator Evaluator[T]
}

// Assessment binds identity and execution immutably. Reconfiguration creates a
// new binding; decorators and suites consume the complete binding.
type Assessment[T any] struct {
	id        AssessmentID
	evaluator Evaluator[T]
}

func NewAssessment[T any](config AssessmentConfig[T]) (Assessment[T], error) {
	if err := config.ID.Validate(); err != nil {
		return Assessment[T]{}, err
	}
	if lo.IsNil(config.Evaluator) {
		return Assessment[T]{}, fmt.Errorf("%w: assessment %q evaluator is nil", ErrInvalidAssessment, config.ID)
	}
	return Assessment[T]{id: config.ID, evaluator: config.Evaluator}, nil
}

func (a Assessment[T]) ID() AssessmentID { return a.id }

func (a Assessment[T]) Evaluate(ctx context.Context, subject T) (Report, error) {
	if !a.Valid() {
		return Report{}, fmt.Errorf("%w: assessment was not constructed with NewAssessment", ErrInvalidAssessment)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	report, err := a.evaluator.Evaluate(ctx, subject)
	if err == nil {
		err = report.Validate()
	}
	if err != nil {
		return Report{}, err
	}
	return report, nil
}

// Valid rejects the zero Assessment. NewAssessment admits identity and
// evaluator once, so presence is enough.
func (a Assessment[T]) Valid() bool { return a.evaluator != nil }

func (a Assessment[T]) run(ctx context.Context, subject T) AssessmentResult {
	if err := ctx.Err(); err != nil {
		return AssessmentResult{ID: a.id, Err: errors.Join(ErrNotEvaluated, err)}
	}
	report, err := a.Evaluate(ctx, subject)
	if err != nil {
		return AssessmentResult{ID: a.id, Err: err}
	}
	return AssessmentResult{ID: a.id, Report: &report}
}

// AssessmentStatus describes execution, independently of a quality verdict.
type AssessmentStatus string

const (
	AssessmentCompleted    AssessmentStatus = "completed"
	AssessmentFailed       AssessmentStatus = "failed"
	AssessmentCanceled     AssessmentStatus = "canceled"
	AssessmentNotEvaluated AssessmentStatus = "not_evaluated"
)

// AssessmentResult has a Report only after successful evaluation. A failed
// evaluator's returned Report is discarded under the Evaluator contract.
// ErrNotEvaluated marks work that never started, preserving any cause through
// errors.Is. Status is derived from these facts rather than stored separately.
type AssessmentResult struct {
	ID     AssessmentID
	Report *Report
	Err    error
}

// Status is zero when result facts are missing or competing.
func (a AssessmentResult) Status() AssessmentStatus {
	if (a.Report != nil) == (a.Err != nil) {
		return ""
	}
	switch {
	case errors.Is(a.Err, ErrNotEvaluated):
		return AssessmentNotEvaluated
	case errors.Is(a.Err, context.Canceled), errors.Is(a.Err, context.DeadlineExceeded):
		return AssessmentCanceled
	case a.Err != nil:
		return AssessmentFailed
	default:
		return AssessmentCompleted
	}
}

func (a AssessmentResult) Validate() error {
	if err := a.ID.Validate(); err != nil {
		return err
	}
	if a.Status() == "" {
		return fmt.Errorf("%w: assessment %q requires either a report or an error", ErrInvalidAssessment, a.ID)
	}
	if a.Report != nil {
		return a.Report.Validate()
	}
	return nil
}

func (a AssessmentResult) clone() AssessmentResult {
	if a.Report != nil {
		report := a.Report.cloneValid()
		a.Report = &report
	}
	return a
}
