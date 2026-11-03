package fs

import (
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

func mutationError(operation string, response any, cause error) error {
	_, containsFailure := errors.AsType[*tool.Failure](cause)
	_, containsEvidence := errors.AsType[*tool.CallError](cause)
	if containsFailure || containsEvidence || !errors.Is(cause, ErrMutationRejected) {
		return toolresult.WithEvidence(operation, response, cause)
	}
	cause = fmt.Errorf("%s: %w", operation, cause)
	return toolresult.Correctable(cause.Error(), cause)
}
