package tool

import (
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

var ErrInvalidCallError = errors.New("tool: invalid call error")

// CallErrorConfig attaches observed execution evidence to an error. Evidence
// may acknowledge effects without establishing the invocation's final outcome.
type CallErrorConfig struct {
	Cause    error
	Evidence chat.ToolOutput
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
	evidence chat.ToolOutput
}

func NewCallError(config CallErrorConfig) (*CallError, error) {
	c := &CallError{cause: config.Cause, evidence: config.Evidence.Clone()}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
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

func (c *CallError) Validate() error {
	if c == nil || lo.IsNil(c.cause) {
		return fmt.Errorf("%w: cause is required", ErrInvalidCallError)
	}
	if err := c.evidence.Validate(); err != nil {
		return fmt.Errorf("%w: evidence: %w", ErrInvalidCallError, err)
	}
	return nil
}

// Evidence returns an independent snapshot of observations made before the
// error. Missing observations do not prove that effects did not occur.
func (c *CallError) Evidence() chat.ToolOutput {
	if c == nil {
		return chat.ToolOutput{}
	}
	return c.evidence.Clone()
}
