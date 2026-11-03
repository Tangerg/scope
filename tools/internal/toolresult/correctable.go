package toolresult

import (
	"errors"

	"github.com/Tangerg/scope/core/tool"
)

// Correctable settles an error the model caused and can fix by changing its
// arguments. Feedback is the model-visible text; callers choose it because a
// cause may carry host detail the model must not see.
func Correctable(feedback string, cause error) error {
	failure, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: cause, Output: tool.NewTextOutput(feedback)})
	if err != nil {
		return errors.Join(cause, err)
	}
	return failure
}
