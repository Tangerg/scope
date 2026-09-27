package fs

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

func mutationError(operation string, response any, cause error) error {
	_, definite := errors.AsType[*tool.Failure](cause)
	_, observed := errors.AsType[*tool.CallError](cause)
	if definite || observed || !errors.Is(cause, ErrMutationRejected) {
		return toolresult.WithEvidence(operation, response, cause)
	}
	cause = fmt.Errorf("%s: %w", operation, cause)
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: cause, Output: chat.NewTextToolOutput(cause.Error()),
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	return failure
}
