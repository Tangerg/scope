package interaction

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/agent"
)

// ToolCallRef identifies one logical call by its requesting Interaction Process,
// one-based model call sequence and zero-based position in that response.
// It is immutable and comparable. Provider call IDs, execution attempts and
// managed child identities do not participate in equality. A reference identifies
// a call; it does not prove execution, settlement or permission to replay it.
type ToolCallRef struct {
	processID         agent.ProcessID
	modelCallSequence uint64
	toolCallIndex     uint32
}

func (t ToolCallRef) ProcessID() agent.ProcessID { return t.processID }

func (t ToolCallRef) ModelCallSequence() uint64 { return t.modelCallSequence }

func (t ToolCallRef) ToolCallIndex() uint32 { return t.toolCallIndex }

func (t ToolCallRef) Valid() bool {
	return t.processID.Valid() && t.modelCallSequence > 0
}

// String is the canonical, lossless text encoding used by MarshalText.
// The zero value has an empty encoding and cannot be marshaled.
func (t ToolCallRef) String() string {
	if !t.Valid() {
		return ""
	}
	return t.processID.String() + "/" + strconv.FormatUint(t.modelCallSequence, 10) + "/" + strconv.FormatUint(uint64(t.toolCallIndex), 10)
}

func (t ToolCallRef) MarshalText() ([]byte, error) {
	if !t.Valid() {
		return nil, ErrInvalidToolCallRef
	}
	return []byte(t.String()), nil
}

func (t *ToolCallRef) UnmarshalText(text []byte) error {
	if t == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidToolCallRef)
	}
	value, err := ParseToolCallRef(string(text))
	if err != nil {
		return err
	}
	*t = value
	return nil
}

func (ToolCallRef) JSONSchemaAlias() any { return "" }

// ParseToolCallRef accepts only the canonical text encoding. Parsing restores a
// reference, never an execution authority or a managed child identity.
func ParseToolCallRef(value string) (ToolCallRef, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 3 {
		return ToolCallRef{}, ErrInvalidToolCallRef
	}
	processID, err := agent.ParseProcessID(parts[0])
	if err != nil {
		return ToolCallRef{}, fmt.Errorf("%w: process: %w", ErrInvalidToolCallRef, err)
	}
	sequence, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return ToolCallRef{}, fmt.Errorf("%w: model call sequence: %w", ErrInvalidToolCallRef, err)
	}
	index, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil {
		return ToolCallRef{}, fmt.Errorf("%w: tool call index: %w", ErrInvalidToolCallRef, err)
	}
	reference := ToolCallRef{processID: processID, modelCallSequence: sequence, toolCallIndex: uint32(index)}
	if !reference.Valid() || reference.String() != value {
		return ToolCallRef{}, ErrInvalidToolCallRef
	}
	return reference, nil
}
