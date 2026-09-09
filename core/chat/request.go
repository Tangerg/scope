package chat

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
)

// ErrInvalidRequest identifies an invalid complete model input.
var ErrInvalidRequest = errors.New("chat: invalid request")

// Request is the complete provider-neutral input to a chat model. It contains
// only serializable protocol values; executable tools and invocation state are
// supplied separately by higher-level runtimes. Construction and cloning
// snapshot every mutable nested protocol value before middleware or providers
// receive it.
type Request struct {
	Messages   []Message        `json:"messages"`
	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolChoice *ToolChoice      `json:"tool_choice,omitempty"`
	Options    Options          `json:"options,omitzero"`
}

func (r *Request) Clone() *Request {
	if r == nil {
		return nil
	}
	clone := &Request{
		Messages:   make([]Message, len(r.Messages)),
		Tools:      make([]ToolDefinition, len(r.Tools)),
		ToolChoice: r.ToolChoice.Clone(),
		Options:    r.Options.Clone(),
	}
	for index := range r.Messages {
		clone.Messages[index] = r.Messages[index].Clone()
	}
	for index := range r.Tools {
		clone.Tools[index] = r.Tools[index].Clone()
	}
	return clone
}

// NewRequest snapshots and validates a complete conversation, including its
// leading system prefix and tool-call consistency.
func NewRequest(messages ...Message) (*Request, error) {
	r := &Request{Messages: slices.Clone(messages)}
	for index := range r.Messages {
		r.Messages[index] = r.Messages[index].Clone()
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Request) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil request", ErrInvalidRequest)
	}
	if len(r.Messages) == 0 {
		return fmt.Errorf("%w: at least one message is required", ErrInvalidRequest)
	}
	for i := range r.Messages {
		if err := r.Messages[i].Validate(); err != nil {
			return fmt.Errorf("%w: messages[%d]: %w", ErrInvalidRequest, i, err)
		}
		if r.Messages[i].Role == RoleSystem && i > 0 && r.Messages[i-1].Role != RoleSystem {
			return fmt.Errorf("%w: system messages must form a leading prefix", ErrInvalidRequest)
		}
	}

	toolNames := make(map[string]struct{}, len(r.Tools))
	for i := range r.Tools {
		if err := r.Tools[i].Validate(); err != nil {
			return fmt.Errorf("%w: tools[%d]: %w", ErrInvalidRequest, i, err)
		}
		if _, duplicate := toolNames[r.Tools[i].Name]; duplicate {
			return fmt.Errorf("%w: duplicate tool name %q", ErrInvalidRequest, r.Tools[i].Name)
		}
		toolNames[r.Tools[i].Name] = struct{}{}
	}
	if r.ToolChoice != nil {
		if len(r.Tools) == 0 {
			return fmt.Errorf("%w: tool_choice requires at least one tool", ErrInvalidRequest)
		}
		if err := r.ToolChoice.Validate(); err != nil {
			return fmt.Errorf("%w: tool_choice: %w", ErrInvalidRequest, err)
		}
		if r.ToolChoice.Mode == ToolChoiceNamed {
			if _, exists := toolNames[r.ToolChoice.Name]; !exists {
				return fmt.Errorf("%w: tool_choice names undefined tool %q", ErrInvalidRequest, r.ToolChoice.Name)
			}
		}
	}
	if err := r.Options.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return nil
}

func (r Request) MarshalJSON() ([]byte, error) {
	if err := (&r).Validate(); err != nil {
		return nil, err
	}
	type wireRequest Request
	return json.Marshal(wireRequest(r))
}

func (r *Request) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidRequest)
	}
	type wireRequest Request
	var decoded wireRequest
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidRequest, err)
	}
	candidate := Request(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}
