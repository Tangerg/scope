package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"
)

// AssessmentID identifies an evaluation independently of whether it succeeds.
// The Host keeps this identity stable when comparing the same assessment.
type AssessmentID string

func (a AssessmentID) Validate() error {
	if a == "" || strings.TrimSpace(string(a)) != string(a) {
		return fmt.Errorf("%w: assessment id must be non-empty without surrounding whitespace", ErrInvalidAssessment)
	}
	return nil
}

// Assessment binds a declared identity to the one atomic evaluation call.
type Assessment[T any] struct {
	ID        AssessmentID
	Evaluator Evaluator[T]
}

func (a Assessment[T]) Validate() error {
	if err := a.ID.Validate(); err != nil {
		return err
	}
	if lo.IsNil(a.Evaluator) {
		return fmt.Errorf("%w: assessment %q evaluator is nil", ErrInvalidAssessment, a.ID)
	}
	return nil
}

func (a Assessment[T]) run(ctx context.Context, subject T) AssessmentResult {
	if err := ctx.Err(); err != nil {
		return AssessmentResult{ID: a.ID, Err: errors.Join(ErrNotEvaluated, err)}
	}
	report, err := a.Evaluator.Evaluate(ctx, subject)
	if err == nil {
		err = report.Validate()
	}
	if err != nil {
		return AssessmentResult{ID: a.ID, Err: err}
	}
	return AssessmentResult{ID: a.ID, Report: &report}
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
