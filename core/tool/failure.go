package tool

import (
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/internal/errscope"
)

var ErrInvalidFailure = errors.New("tool: invalid failure")

// FailureKind describes the current invocation, independently of its diagnostic
// cause. Neither kind assigns retry or runtime control policy.
type FailureKind string

const (
	// FailureKindFailed is a definite unsuccessful outcome. It does not assert
	// that execution began and may describe acknowledged partial effects.
	FailureKindFailed FailureKind = "failed"
	// FailureKindRejected means the invocation was refused permission to execute.
	FailureKindRejected FailureKind = "rejected"
)

// FailureConfig describes one unsuccessful invocation. NewFailure snapshots
// Output and retains Cause only for explicit diagnostic inspection.
type FailureConfig struct {
	Kind   FailureKind
	Output Output
	Cause  error
}

// Failure owns one complete unsuccessful outcome of the current Tool invocation.
// Output is explicitly public to the model. Cause is diagnostic only: it cannot
// change Kind, substitute another invocation's output, or issue a control signal.
// Ordinary errors, including CallError with execution evidence, do not establish
// a definite outcome.
type Failure struct {
	kind   FailureKind
	cause  error
	output Output
}

// NewFailure snapshots public output. Failed outcomes require a cause; rejection
// may be a policy decision without an error. Wrapping the returned Failure with
// %w preserves its outcome; inspect Cause explicitly for internal diagnostics.
func NewFailure(config FailureConfig) (*Failure, error) {
	switch config.Kind {
	case FailureKindFailed:
		if lo.IsNil(config.Cause) {
			return nil, fmt.Errorf("%w: failed outcome requires a cause", ErrInvalidFailure)
		}
	case FailureKindRejected:
		if config.Cause != nil && lo.IsNil(config.Cause) {
			return nil, fmt.Errorf("%w: rejection cause is typed nil", ErrInvalidFailure)
		}
	default:
		return nil, fmt.Errorf("%w: kind %q", ErrInvalidFailure, config.Kind)
	}
	output := config.Output.Clone()
	if err := output.Validate(); err != nil {
		return nil, fmt.Errorf("%w: output: %w", ErrInvalidFailure, err)
	}
	return &Failure{kind: config.Kind, cause: config.Cause, output: output}, nil
}

func (f *Failure) Error() string {
	if f == nil || f.kind == "" {
		return ErrInvalidFailure.Error()
	}
	if lo.IsNil(f.cause) {
		return "tool: " + string(f.kind)
	}
	return f.cause.Error()
}

// Cause does not participate in errors.Is or errors.As. Transparent error
// wrapping surrounds Failure; diagnostic causes are behind the outcome boundary.
func (f *Failure) Cause() error {
	if f == nil {
		return nil
	}
	return f.cause
}

func (f *Failure) Kind() FailureKind {
	if f == nil {
		return ""
	}
	return f.kind
}

// Validate rejects nil and zero values crossing a Tool boundary. NewFailure is
// the only way to build any other value, and it admits the whole outcome.
func (f *Failure) Validate() error {
	if f == nil || f.kind == "" {
		return ErrInvalidFailure
	}
	return nil
}

// Output returns an independent copy of the complete failure output.
func (f *Failure) Output() Output {
	if f == nil {
		return Output{}
	}
	return f.output.Clone()
}

// ClassifyFailure finds the complete outcome of the current invocation.
// Transparent wrapping preserves one Failure, but joined independent errors
// require their enclosing owner to establish an outcome. Diagnostic traversal
// with errors.As cannot distinguish those invocation boundaries.
// Nil, invalid, and ambiguous failures establish no definite outcome.
func ClassifyFailure(err error) (*Failure, bool) {
	failure, found := errscope.Find[*Failure](err)
	if !found || failure.Validate() != nil {
		return nil, false
	}
	return failure, true
}
