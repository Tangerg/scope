package interaction

import (
	"fmt"
	"strconv"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

const delegateChildKeyNamespace = "interaction.delegate"

func DelegateChildKey(modelCallSequence uint64, toolCall chat.ToolCall) (run.ChildKey, error) {
	if modelCallSequence == 0 {
		return run.ChildKey{}, fmt.Errorf("%w: model call sequence is required", ErrInvalidDelegate)
	}
	if err := toolCall.Validate(); err != nil {
		return run.ChildKey{}, fmt.Errorf("%w: ToolCall: %w", ErrInvalidDelegate, err)
	}
	return run.DeriveChildKey(delegateChildKeyNamespace, strconv.FormatUint(modelCallSequence, 10), toolCall.ID, toolCall.Name)
}
