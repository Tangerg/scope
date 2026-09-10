package tool

import (
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

var ErrInvalidFailure = errors.New("tool: invalid failure")

// Failure preserves a known tool failure's complete model-visible output and
// its original cause. It assigns no retry or control-flow policy. Runtimes may
// expose Output as an error ToolResult after applying their control-plane rules.
// Ordinary errors remain appropriate when no structured failure output exists.
type Failure struct {
	cause  error
	output chat.ToolOutput
}

// NewFailure validates and snapshots output so error wrapping cannot lose its
// text, media, or structured details. cause must be non-nil.
func NewFailure(cause error, output chat.ToolOutput) (*Failure, error) {
	if lo.IsNil(cause) {
		return nil, fmt.Errorf("%w: cause must not be nil", ErrInvalidFailure)
	}
	if err := output.Validate(); err != nil {
		return nil, fmt.Errorf("%w: output: %w", ErrInvalidFailure, err)
	}
	return &Failure{cause: cause, output: output.Clone()}, nil
}

func (f *Failure) Error() string {
	if f == nil || f.cause == nil {
		return ErrInvalidFailure.Error()
	}
	return f.cause.Error()
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.cause
}

// Output returns an independent copy of the complete failure output.
func (f *Failure) Output() chat.ToolOutput {
	if f == nil {
		return chat.ToolOutput{}
	}
	return f.output.Clone()
}
