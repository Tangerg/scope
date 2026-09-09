package chat

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

var (
	// ErrInvalidMessage identifies a message whose role, parts, or metadata
	// violate the portable conversation contract.
	ErrInvalidMessage = errors.New("chat: invalid message")
	// ErrInvalidPart identifies a part whose kind and active payload disagree.
	ErrInvalidPart = errors.New("chat: invalid part")
	// ErrInvalidToolCall identifies an incomplete tool invocation request.
	ErrInvalidToolCall = errors.New("chat: invalid tool call")
	// ErrInvalidToolOutput identifies a malformed tool execution payload.
	ErrInvalidToolOutput = errors.New("chat: invalid tool output")
	// ErrInvalidToolResult identifies a result that cannot answer a tool call.
	ErrInvalidToolResult = errors.New("chat: invalid tool result")
)

// Role identifies a message's participant in a conversation.
type Role string

const (
	// RoleSystem carries model instructions.
	RoleSystem Role = "system"
	// RoleUser carries user input.
	RoleUser Role = "user"
	// RoleAssistant carries model output.
	RoleAssistant Role = "assistant"
	// RoleTool carries results for tool calls requested by the assistant.
	RoleTool Role = "tool"
)

func (r Role) Valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

// Message is one provider-neutral conversation entry. Parts retain their order
// so interleaved assistant text, reasoning, and tool calls round-trip. Clone
// recursively owns every mutable protocol value. Text projection concatenates
// only text parts and deliberately ignores reasoning, media, and tool payloads.
type Message struct {
	Role     Role         `json:"role"`
	Parts    []Part       `json:"parts"`
	Metadata metadata.Map `json:"metadata,omitzero"`
}

func (m Message) Clone() Message {
	clone := Message{
		Role:     m.Role,
		Parts:    make([]Part, len(m.Parts)),
		Metadata: m.Metadata.Clone(),
	}
	for index := range m.Parts {
		clone.Parts[index] = m.Parts[index].Clone()
	}
	return clone
}

// NewSystemMessage creates an instruction message suitable for the leading
// system prefix required by Request.
func NewSystemMessage(text string) Message {
	return Message{Role: RoleSystem, Parts: []Part{NewTextPart(text)}}
}

// NewUserMessage snapshots ordered text and media input parts.
func NewUserMessage(parts ...Part) Message {
	return Message{Role: RoleUser, Parts: slices.Clone(parts)}
}

// NewAssistantMessage snapshots ordered model output parts without collapsing
// reasoning, refusals, or tool calls into text.
func NewAssistantMessage(parts ...Part) Message {
	return Message{Role: RoleAssistant, Parts: slices.Clone(parts)}
}

// NewToolMessage preserves one result part per call so a provider can correlate
// a returned batch without inspecting untyped content.
func NewToolMessage(results ...ToolResult) Message {
	parts := make([]Part, len(results))
	for i := range results {
		parts[i] = NewToolResultPart(results[i])
	}
	return Message{Role: RoleTool, Parts: parts}
}

func (m *Message) Text() string {
	if m == nil {
		return ""
	}
	var text strings.Builder
	for i := range m.Parts {
		if m.Parts[i].Kind == PartText {
			text.WriteString(m.Parts[i].Text)
		}
	}
	return text.String()
}

func (m Message) Validate() error {
	if !m.Role.Valid() {
		return fmt.Errorf("%w: unknown role %q", ErrInvalidMessage, m.Role)
	}
	if len(m.Parts) == 0 {
		return fmt.Errorf("%w: role %q requires at least one part", ErrInvalidMessage, m.Role)
	}
	if err := m.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidMessage, err)
	}
	for i := range m.Parts {
		part := m.Parts[i]
		if err := part.Validate(); err != nil {
			return fmt.Errorf("%w: parts[%d]: %w", ErrInvalidMessage, i, err)
		}
		if !m.Role.allowsPart(part.Kind) {
			return fmt.Errorf("%w: role %q does not allow part kind %q", ErrInvalidMessage, m.Role, part.Kind)
		}
	}
	return nil
}

// allowsPart reports whether a message with role r may carry a part of the
// given kind.
func (r Role) allowsPart(kind PartKind) bool {
	switch r {
	case RoleSystem:
		return kind == PartText
	case RoleUser:
		return kind == PartText || kind == PartMedia
	case RoleAssistant:
		return kind == PartText || kind == PartMedia || kind == PartReasoning || kind == PartToolCall || kind == PartRefusal
	case RoleTool:
		return kind == PartToolResult
	default:
		return false
	}
}

func (m Message) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	type wireMessage Message
	return json.Marshal(wireMessage(m))
}

func (m *Message) UnmarshalJSON(data []byte) error {
	if m == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidMessage)
	}
	type wireMessage Message
	var decoded wireMessage
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidMessage, err)
	}
	candidate := Message(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*m = candidate
	return nil
}
