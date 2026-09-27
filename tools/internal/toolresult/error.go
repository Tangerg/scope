package toolresult

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// WithEvidence preserves a backend's explicit outcome or evidence before
// projecting its typed response. A normal backend error does not establish a
// final outcome, even when the response acknowledges some effects.
func WithEvidence(operation string, response any, cause error) error {
	cause = fmt.Errorf("%s: %w", operation, cause)
	if failure, ok := errors.AsType[*tool.Failure](cause); ok {
		if err := failure.Validate(); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	if callErr, ok := errors.AsType[*tool.CallError](cause); ok {
		if err := callErr.Validate(); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	encoded, err := jsonv2.Marshal(response)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("encode execution evidence: %w", err))
	}
	evidence, err := chat.NewJSONToolOutput(encoded)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("build execution evidence: %w", err))
	}
	callErr, err := tool.NewCallError(tool.CallErrorConfig{Cause: cause, Evidence: evidence})
	if err != nil {
		return errors.Join(cause, err)
	}
	return callErr
}
