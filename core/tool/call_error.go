package tool

import (
	"errors"
	"fmt"

	"github.com/samber/lo"
)

var ErrInvalidCallError = errors.New("tool: invalid call error")

// CallErrorConfig attaches observed execution evidence to an error. Evidence
// may acknowledge effects without establishing the invocation's final outcome.
type CallErrorConfig struct {
	Cause    error
	Evidence Output
}

// CallError preserves evidence that would otherwise be lost when a Tool returns
// an error. Evidence is available for diagnostics and evaluation; it is not a
// completed result and must not be submitted to a model as a ToolResult.
//
// CallError assigns no outcome or control meaning. Its transparent cause retains
// the original error contract; only Failure establishes a definite unsuccessful
// outcome. Runtimes must not infer retry safety from the presence of evidence.
type CallError struct {
	cause    error
	evidence Output
}

func NewCallError(config CallErrorConfig) (*CallError, error) {
	if lo.IsNil(config.Cause) {
		return nil, fmt.Errorf("%w: cause is required", ErrInvalidCallError)
	}
	evidence := config.Evidence.Clone()
	if err := evidence.Validate(); err != nil {
		return nil, fmt.Errorf("%w: evidence: %w", ErrInvalidCallError, err)
	}
	return &CallError{cause: config.Cause, evidence: evidence}, nil
}

func (c *CallError) Error() string {
	if c == nil || lo.IsNil(c.cause) {
		return ErrInvalidCallError.Error()
	}
	return c.cause.Error()
}

func (c *CallError) Unwrap() error {
	if c == nil {
		return nil
	}
	return c.cause
}

// Validate rejects nil and zero values; NewCallError admits every other one.
func (c *CallError) Validate() error {
	if c == nil || c.cause == nil {
		return fmt.Errorf("%w: cause is required", ErrInvalidCallError)
	}
	return nil
}

// Evidence returns an independent snapshot of observations made before the
// error. Missing observations do not prove that effects did not occur.
func (c *CallError) Evidence() Output {
	if c == nil {
		return Output{}
	}
	return c.evidence.Clone()
}
