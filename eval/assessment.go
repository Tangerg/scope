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
		return AssessmentResult{ID: a.ID, Status: AssessmentNotEvaluated, Err: ErrNotEvaluated}
	}
	report, err := a.Evaluator.Evaluate(ctx, subject)
	if err == nil {
		err = report.Validate()
	}
	if err != nil {
		status := AssessmentFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = AssessmentCanceled
		}
		return AssessmentResult{ID: a.ID, Status: status, Err: err}
	}
	return AssessmentResult{ID: a.ID, Status: AssessmentCompleted, Report: &report}
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
type AssessmentResult struct {
	ID     AssessmentID
	Status AssessmentStatus
	Report *Report
	Err    error
}

func (a AssessmentResult) Validate() error {
	if err := a.ID.Validate(); err != nil {
		return err
	}
	switch a.Status {
	case AssessmentCompleted:
		if a.Err != nil || a.Report == nil {
			return fmt.Errorf("%w: completed assessment %q requires a report and no error", ErrInvalidAssessment, a.ID)
		}
		return a.Report.Validate()
	case AssessmentFailed, AssessmentCanceled, AssessmentNotEvaluated:
		if a.Err == nil || a.Report != nil {
			return fmt.Errorf("%w: incomplete assessment %q requires an error and no report", ErrInvalidAssessment, a.ID)
		}
		canceled := errors.Is(a.Err, context.Canceled) || errors.Is(a.Err, context.DeadlineExceeded)
		if a.Status == AssessmentCanceled && !canceled || a.Status == AssessmentFailed && canceled {
			return fmt.Errorf("%w: assessment %q status does not match its cancellation error", ErrInvalidAssessment, a.ID)
		}
	default:
		return fmt.Errorf("%w: assessment %q has unknown status %q", ErrInvalidAssessment, a.ID, a.Status)
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
