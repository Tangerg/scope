package mistral

import (
	"encoding/binary"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	thinkingFrameMagic      = "MSTH"
	thinkingFrameLengthSize = 4
	thinkingFrameHeaderSize = len(thinkingFrameMagic) + thinkingFrameLengthSize
	thinkingPartIdentityKey = "mistral/thinking_part_identity"
	thinkingFieldType       = "type"
	thinkingFieldText       = "text"
	thinkingFieldChildren   = "thinking"
	thinkingFieldClosed     = "closed"
)

type thinkingClosure uint8

const (
	thinkingClosureAbsent thinkingClosure = iota
	thinkingClosureOpen
	thinkingClosureClosed
)

func (t thinkingClosure) MarshalJSON() ([]byte, error) {
	switch t {
	case thinkingClosureOpen:
		return jsonv2.Marshal(false)
	case thinkingClosureClosed:
		return jsonv2.Marshal(true)
	default:
		return nil, errors.New("thinking closure has no boolean value")
	}
}

func (t *thinkingClosure) UnmarshalJSON(data []byte) error {
	var value *bool
	if err := jsonv2.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == nil {
		return errors.New("thinking closed must be a boolean")
	}
	*t = thinkingClosureOpen
	if *value {
		*t = thinkingClosureClosed
	}
	return nil
}

type thinkingEnvelope struct {
	Closed thinkingClosure            `json:"closed,omitzero"`
	Extra  map[string]json.RawMessage `json:"extra,omitzero"`
}

func (t thinkingEnvelope) validate() error {
	if t.Closed > thinkingClosureClosed {
		return errors.New("invalid thinking closure")
	}
	for _, field := range []string{thinkingFieldType, thinkingFieldChildren, thinkingFieldClosed} {
		if _, exists := t.Extra[field]; exists {
			return fmt.Errorf("thinking extension %q has a typed owner", field)
		}
	}
	return nil
}

func (t *thinkingEnvelope) apply(delta thinkingEnvelope) {
	if delta.Closed != thinkingClosureAbsent {
		t.Closed = delta.Closed
	}
	if len(delta.Extra) == 0 {
		return
	}
	if t.Extra == nil {
		t.Extra = make(map[string]json.RawMessage, len(delta.Extra))
	}
	maps.Copy(t.Extra, delta.Extra)
}

type thinkingContentState struct {
	Type  contentType                `json:"type"`
	Extra map[string]json.RawMessage `json:"extra,omitzero"`
}

func (t thinkingContentState) validate() error {
	if t.Type == "" {
		return errors.New("thinking content type is required")
	}
	if _, exists := t.Extra[thinkingFieldType]; exists {
		return errors.New("thinking content type has a typed owner")
	}
	if _, exists := t.Extra[thinkingFieldText]; exists && t.Type == contentTypeText {
		return errors.New("thinking state must not contain Core-owned text")
	}
	return nil
}

func (t thinkingContentState) replay(text string) (json.RawMessage, error) {
	fields := maps.Clone(t.Extra)
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	var err error
	fields[thinkingFieldType], err = jsonv2.Marshal(t.Type)
	if err != nil {
		return nil, err
	}
	if t.Type == contentTypeText {
		fields[thinkingFieldText], err = jsonv2.Marshal(text)
		if err != nil {
			return nil, err
		}
	} else if text != "" {
		return nil, errors.New("thinking content has no text field")
	}
	return jsonv2.Marshal(fields)
}

func parseThinkingContent(raw json.RawMessage) (thinkingContentState, string, error) {
	var wire struct {
		Type  contentType                `json:"type"`
		Extra map[string]json.RawMessage `json:",embed"`
	}
	if err := jsonv2.Unmarshal(raw, &wire); err != nil {
		return thinkingContentState{}, "", err
	}
	state := thinkingContentState{Type: wire.Type, Extra: wire.Extra}
	text := ""
	if state.Type == contentTypeText {
		var value *string
		if err := jsonv2.Unmarshal(state.Extra[thinkingFieldText], &value); err != nil {
			return thinkingContentState{}, "", fmt.Errorf("thinking text: %w", err)
		}
		if value == nil {
			return thinkingContentState{}, "", errors.New("thinking text is required")
		}
		text = *value
		delete(state.Extra, thinkingFieldText)
	}
	if err := state.validate(); err != nil {
		return thinkingContentState{}, "", err
	}
	return state, text, nil
}

// Core owns text. The replay frame contains only a child's native state and,
// on the last child, the envelope delta that advances its block boundary.
type thinkingPartState struct {
	Block    uint64                `json:"block"`
	Content  *thinkingContentState `json:"content,omitzero"`
	Envelope thinkingEnvelope      `json:"envelope,omitzero"`
}

func (t thinkingPartState) validate() error {
	if t.Block == 0 {
		return errors.New("thinking block identity is required")
	}
	if t.Content != nil {
		if err := t.Content.validate(); err != nil {
			return err
		}
	}
	return t.Envelope.validate()
}

func (t thinkingPartState) closed() bool {
	return t.Envelope.Closed == thinkingClosureClosed
}

type thinkingWireChunk struct {
	Type     contentType                `json:"type"`
	Thinking []json.RawMessage          `json:"thinking"`
	Closed   thinkingClosure            `json:"closed,omitzero"`
	Extra    map[string]json.RawMessage `json:",embed"`
}

func (t thinkingWireChunk) deltas(block uint64) ([]corechat.PartDelta, error) {
	if t.Type != contentTypeThinking || t.Thinking == nil {
		return nil, errors.New("thinking chunk requires a thinking array")
	}
	parts := make([]corechat.PartDelta, 0, max(1, len(t.Thinking)))
	for index := range max(1, len(t.Thinking)) {
		state := thinkingPartState{Block: block}
		text := ""
		if len(t.Thinking) > 0 {
			content, value, err := parseThinkingContent(t.Thinking[index])
			if err != nil {
				return nil, fmt.Errorf("thinking[%d]: %w", index, err)
			}
			state.Content = &content
			text = value
		}
		if index == max(1, len(t.Thinking))-1 {
			state.Envelope = thinkingEnvelope{Closed: t.Closed, Extra: t.Extra}
		}
		frame, err := encodeThinkingFrame(state)
		if err != nil {
			return nil, err
		}
		parts = append(parts, corechat.NewReasoningDelta(text, frame))
	}
	return parts, nil
}

func encodeThinkingFrame(state thinkingPartState) ([]byte, error) {
	if err := state.validate(); err != nil {
		return nil, err
	}
	raw, err := jsonv2.Marshal(state)
	if err != nil {
		return nil, err
	}
	if uint64(len(raw)) > uint64(^uint32(0)) {
		return nil, errors.New("mistral: thinking chunk exceeds framing limit")
	}
	frame := make([]byte, thinkingFrameHeaderSize+len(raw))
	copy(frame, thinkingFrameMagic)
	binary.BigEndian.PutUint32(frame[len(thinkingFrameMagic):thinkingFrameHeaderSize], uint32(len(raw)))
	copy(frame[thinkingFrameHeaderSize:], raw)
	return frame, nil
}

func decodeThinkingFrame(signature []byte) (thinkingPartState, bool, error) {
	if len(signature) < len(thinkingFrameMagic) || string(signature[:len(thinkingFrameMagic)]) != thinkingFrameMagic {
		return thinkingPartState{}, false, nil
	}
	if len(signature) < thinkingFrameHeaderSize {
		return thinkingPartState{}, true, errors.New("truncated thinking frame header")
	}
	length := int(binary.BigEndian.Uint32(signature[len(thinkingFrameMagic):thinkingFrameHeaderSize]))
	if length != len(signature)-thinkingFrameHeaderSize {
		return thinkingPartState{}, true, errors.New("thinking frame length does not match the part state")
	}
	var state *thinkingPartState
	if err := jsonv2.Unmarshal(signature[thinkingFrameHeaderSize:], &state, jsonv2.RejectUnknownMembers(true)); err != nil {
		return thinkingPartState{}, true, fmt.Errorf("decode thinking state: %w", err)
	}
	if state == nil {
		return thinkingPartState{}, true, errors.New("thinking state must be an object")
	}
	if err := state.validate(); err != nil {
		return thinkingPartState{}, true, err
	}
	return *state, true, nil
}

type thinkingReplay struct {
	block    uint64
	content  []json.RawMessage
	envelope thinkingEnvelope
}

func (t *thinkingReplay) add(state thinkingPartState, text string) error {
	if state.Content == nil {
		if text != "" {
			return errors.New("thinking envelope has no text field")
		}
	} else {
		raw, err := state.Content.replay(text)
		if err != nil {
			return err
		}
		t.content = append(t.content, raw)
	}
	t.envelope.apply(state.Envelope)
	return nil
}

func (t *thinkingReplay) chunk() (json.RawMessage, error) {
	content := t.content
	if content == nil {
		content = []json.RawMessage{}
	}
	return jsonv2.Marshal(thinkingWireChunk{
		Type: contentTypeThinking, Thinking: content,
		Closed: t.envelope.Closed, Extra: t.envelope.Extra,
	})
}
