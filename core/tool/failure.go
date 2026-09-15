package tool

import (
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
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
	Output chat.ToolOutput
	Cause  error
}

// Failure owns one complete unsuccessful outcome of the current Tool invocation.
// Output is explicitly public to the model. Cause is diagnostic only: it cannot
// change Kind, substitute another invocation's output, or issue a control signal.
// Ordinary errors do not establish a definite outcome.
type Failure struct {
	kind   FailureKind
	cause  error
	output chat.ToolOutput
}

// NewFailure snapshots public output. Failed outcomes require a cause; rejection
// may be a policy decision without an error. Wrapping the returned Failure with
// %w preserves its outcome; inspect Cause explicitly for internal diagnostics.
func NewFailure(config FailureConfig) (*Failure, error) {
	failure := &Failure{kind: config.Kind, cause: config.Cause, output: config.Output}
	if err := failure.Validate(); err != nil {
		return nil, err
	}
	failure.output = config.Output.Clone()
	return failure, nil
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

// Validate rejects zero and typed-nil values crossing a Tool boundary.
func (f *Failure) Validate() error {
	if f == nil {
		return ErrInvalidFailure
	}
	switch f.kind {
	case FailureKindFailed:
		if lo.IsNil(f.cause) {
			return fmt.Errorf("%w: failed outcome requires a cause", ErrInvalidFailure)
		}
	case FailureKindRejected:
		if f.cause != nil && lo.IsNil(f.cause) {
			return fmt.Errorf("%w: rejection cause is typed nil", ErrInvalidFailure)
		}
	default:
		return fmt.Errorf("%w: kind %q", ErrInvalidFailure, f.kind)
	}
	if err := f.output.Validate(); err != nil {
		return fmt.Errorf("%w: output: %w", ErrInvalidFailure, err)
	}
	return nil
}

// Output returns an independent copy of the complete failure output.
func (f *Failure) Output() chat.ToolOutput {
	if f == nil {
		return chat.ToolOutput{}
	}
	return f.output.Clone()
}
