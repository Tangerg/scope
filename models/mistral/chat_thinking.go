package mistral

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
)

const (
	thinkingFrameMagic      = "MSTH"
	thinkingFrameLengthSize = 4
	thinkingFrameHeaderSize = len(thinkingFrameMagic) + thinkingFrameLengthSize
	thinkingPartIdentityKey = "mistral/thinking_part_identity"
)

// A part owns one native child; envelope fields appear only on the last child
// of the wire chunk. Core owns text, while replay state owns native-only values.
type thinkingPartState struct {
	Block   uint64                     `json:"block"`
	Content map[string]json.RawMessage `json:"content,omitzero"`
	Fields  map[string]json.RawMessage `json:"fields,omitzero"`
}

func (t thinkingPartState) validate() error {
	if t.Block == 0 {
		return errors.New("thinking block identity is required")
	}
	if t.Content != nil {
		kind, err := t.contentType()
		if err != nil {
			return err
		}
		if _, exists := t.Content["text"]; kind == contentTypeText && exists {
			return errors.New("thinking state must not contain Core-owned text")
		}
	}
	for _, field := range []string{"type", "thinking"} {
		if _, exists := t.Fields[field]; exists {
			return fmt.Errorf("thinking envelope field %q is owned by the content mapping", field)
		}
	}
	if closed, exists := t.Fields["closed"]; exists {
		var value bool
		if bytes.Equal(bytes.TrimSpace(closed), []byte("null")) {
			return errors.New("thinking closed must be a boolean")
		}
		if err := jsonv2.Unmarshal(closed, &value); err != nil {
			return fmt.Errorf("thinking closed: %w", err)
		}
	}
	return nil
}

func (t thinkingPartState) contentType() (contentType, error) {
	var kind contentType
	if err := jsonv2.Unmarshal(t.Content["type"], &kind); err != nil {
		return "", fmt.Errorf("thinking content type: %w", err)
	}
	if kind == "" {
		return "", errors.New("thinking content type is required")
	}
	return kind, nil
}

func (t thinkingPartState) closed() bool {
	return bytes.Equal(bytes.TrimSpace(t.Fields["closed"]), []byte("true"))
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
	block   uint64
	content []json.RawMessage
	fields  map[string]json.RawMessage
}

func (t *thinkingReplay) add(state thinkingPartState, text string) error {
	if state.Content == nil {
		if text != "" {
			return errors.New("thinking envelope has no text field")
		}
	} else {
		kind, err := state.contentType()
		if err != nil {
			return err
		}
		content := maps.Clone(state.Content)
		if kind == contentTypeText {
			content["text"], err = jsonv2.Marshal(text)
			if err != nil {
				return err
			}
		} else if text != "" {
			return errors.New("thinking content has no text field")
		}
		raw, err := jsonv2.Marshal(content)
		if err != nil {
			return err
		}
		t.content = append(t.content, raw)
	}
	if t.fields == nil {
		t.fields = make(map[string]json.RawMessage)
	}
	// These are chronological native envelope deltas, never text copies.
	maps.Copy(t.fields, state.Fields)
	return nil
}

func (t *thinkingReplay) chunk() (json.RawMessage, error) {
	fields := maps.Clone(t.fields)
	fields["type"] = json.RawMessage(`"thinking"`)
	content := t.content
	if content == nil {
		content = []json.RawMessage{}
	}
	encoded, err := jsonv2.Marshal(content)
	if err != nil {
		return nil, err
	}
	fields["thinking"] = encoded
	return jsonv2.Marshal(fields)
}
