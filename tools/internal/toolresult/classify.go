package toolresult

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/tool"
)

// Classify projects a backend error into the Tool outcome it establishes. An
// outcome or evidence the backend attached wins. An error matching one of the
// correctable sentinels is one the model can fix by changing its arguments, so
// it becomes a definite failure the model sees, unless an outcome is joined
// beside it. Any other error stays uncertain and carries the typed response as
// execution evidence, even when that response acknowledges some effects.
func Classify(operation string, response any, cause error, correctable ...error) error {
	cause = fmt.Errorf("%s: %w", operation, cause)
	if _, definite := tool.ClassifyFailure(cause); definite {
		return cause
	}
	if callErr, ok := errors.AsType[*tool.CallError](cause); ok {
		if err := callErr.Validate(); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	_, joined := errors.AsType[*tool.Failure](cause)
	if !joined && slices.ContainsFunc(correctable, func(target error) bool { return errors.Is(cause, target) }) {
		return tool.FailureFromCause(tool.FailureKindFailed, cause)
	}
	encoded, err := jsonv2.Marshal(response)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("encode execution evidence: %w", err))
	}
	evidence, err := tool.NewJSONOutput(encoded)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("build execution evidence: %w", err))
	}
	callErr, err := tool.NewCallError(tool.CallErrorConfig{Cause: cause, Evidence: evidence})
	if err != nil {
		return errors.Join(cause, err)
	}
	return callErr
}
