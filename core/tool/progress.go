package tool

import (
	"context"

	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/samber/lo"
)

var ErrInvalidProgress = errors.New("tool: invalid progress")

// Progress reports how far one call has advanced. Total is zero when the Tool
// cannot bound its work.
type Progress struct {
	Completed float64 `json:"completed"`
	Total     float64 `json:"total,omitzero"`
	Message   string  `json:"message,omitzero"`
}

func (p Progress) Validate() error {
	if !finiteNonNegative(p.Completed) || !finiteNonNegative(p.Total) {
		return fmt.Errorf("%w: completed and total must be finite and non-negative", ErrInvalidProgress)
	}
	if p.Total != 0 && p.Completed > p.Total {
		return fmt.Errorf("%w: completed %g exceeds total %g", ErrInvalidProgress, p.Completed, p.Total)
	}
	if !utf8.ValidString(p.Message) {
		return fmt.Errorf("%w: message must be valid UTF-8", ErrInvalidProgress)
	}
	return nil
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsInf(value, 1)
}

// ProgressReporter delivers a call's Progress to the driver that asked for it.
// Progress is observation only: it never changes the call's outcome.
type ProgressReporter interface {
	ReportProgress(ctx context.Context, progress Progress) error
}

type ProgressReporterFunc func(ctx context.Context, progress Progress) error

func (p ProgressReporterFunc) ReportProgress(ctx context.Context, progress Progress) error {
	return p(ctx, progress)
}

type progressReporterKey struct{}

// WithProgressReporter installs the driver's observer for calls made with the
// returned context. Progress is observation, like a trace span, so it travels
// with the context; the Invocation stays a value that retains no driver
// capability and can be handed to scheduling policies.
func WithProgressReporter(ctx context.Context, reporter ProgressReporter) context.Context {
	if lo.IsNil(reporter) {
		return ctx
	}
	return context.WithValue(ctx, progressReporterKey{}, reporter)
}

// ReportProgress forwards progress to the observer the driver installed. It is
// a no-op when the driver observes none, so a Tool reports unconditionally.
func ReportProgress(ctx context.Context, progress Progress) error {
	if err := progress.Validate(); err != nil {
		return err
	}
	reporter, observed := ctx.Value(progressReporterKey{}).(ProgressReporter)
	if !observed {
		return nil
	}
	return reporter.ReportProgress(ctx, progress)
}
