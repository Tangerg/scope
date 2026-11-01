package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

const (
	toolChildKeyPrefix     = "interaction.tool."
	delegateChildKeyPrefix = "interaction.delegate.child."
)

// ToolChildKey names the Tool child Process that serves the ToolCall at
// toolCallIndex in the response to modelCallSequence. The Runtime keeps a
// ChildKey unique under its parent, so the key, not the child's input, owns
// the ToolCallRef a Tool child reports.
func ToolChildKey(modelCallSequence uint64, toolCallIndex uint32) (run.ChildKey, error) {
	if modelCallSequence == 0 {
		return run.ChildKey{}, fmt.Errorf("%w: model call sequence is required", ErrInvalidExecutionState)
	}
	return run.ParseChildKey(toolChildKeyPrefix + strconv.FormatUint(modelCallSequence, 10) + "." + strconv.FormatUint(uint64(toolCallIndex), 10))
}

// toolCallRef reads the reference a Tool child's relation carries. Only a
// child keyed by ToolChildKey has one; a root or any other key has none.
func toolCallRef(relation run.ProcessRelation) (ToolCallRef, bool) {
	parent, child := relation.ParentID()
	key, keyed := relation.ChildKey()
	if !child || !keyed {
		return ToolCallRef{}, false
	}
	fields, ok := strings.CutPrefix(key.String(), toolChildKeyPrefix)
	if !ok {
		return ToolCallRef{}, false
	}
	sequenceText, indexText, ok := strings.Cut(fields, ".")
	if !ok {
		return ToolCallRef{}, false
	}
	sequence, sequenceErr := strconv.ParseUint(sequenceText, 10, 64)
	index, indexErr := strconv.ParseUint(indexText, 10, 32)
	if sequenceErr != nil || indexErr != nil {
		return ToolCallRef{}, false
	}
	// One reference has one key: a non-canonical spelling names another child.
	canonical, err := ToolChildKey(sequence, uint32(index))
	if err != nil || canonical != key {
		return ToolCallRef{}, false
	}
	return ToolCallRef{processID: parent, modelCallSequence: sequence, toolCallIndex: uint32(index)}, true
}

func DelegateChildKey(modelCallSequence uint64, toolCall chat.ToolCall) (run.ChildKey, error) {
	if modelCallSequence == 0 {
		return run.ChildKey{}, fmt.Errorf("%w: model call sequence is required", ErrInvalidDelegate)
	}
	if err := toolCall.Validate(); err != nil {
		return run.ChildKey{}, fmt.Errorf("%w: ToolCall: %w", ErrInvalidDelegate, err)
	}
	hash := sha256.New()
	hash.Write([]byte(strconv.FormatUint(modelCallSequence, 10)))
	hash.Write([]byte{0})
	hash.Write([]byte(toolCall.ID))
	hash.Write([]byte{0})
	hash.Write([]byte(toolCall.Name))
	return run.ParseChildKey(delegateChildKeyPrefix + hex.EncodeToString(hash.Sum(nil)))
}
