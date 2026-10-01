package interaction

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// ToolChildKey correlates an ordinary ToolCall with its managed child Process.
func ToolChildKey(modelCallSequence uint64, call chat.ToolCall) (agent.ChildKey, error) {
	if modelCallSequence == 0 || call.Validate() != nil {
		return agent.ChildKey{}, ErrInvalidExecutionState
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", modelCallSequence, call.ID)))
	return agent.ParseChildKey("tool_" + hex.EncodeToString(digest[:]))
}

// DelegateChildKey correlates a Delegate ToolCall with its managed child Process.
func DelegateChildKey(modelCallSequence uint64, toolCall chat.ToolCall) (agent.ChildKey, error) {
	if modelCallSequence == 0 {
		return agent.ChildKey{}, fmt.Errorf("%w: model call sequence is required", ErrInvalidDelegate)
	}
	if err := toolCall.Validate(); err != nil {
		return agent.ChildKey{}, fmt.Errorf("%w: ToolCall: %w", ErrInvalidDelegate, err)
	}
	hash := sha256.New()
	hash.Write([]byte(strconv.FormatUint(modelCallSequence, 10)))
	hash.Write([]byte{0})
	hash.Write([]byte(toolCall.ID))
	hash.Write([]byte{0})
	hash.Write([]byte(toolCall.Name))
	return agent.ParseChildKey("interaction.delegate.child." + hex.EncodeToString(hash.Sum(nil)))
}
