package openai

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	responsesReasoningLengthSize  = 4
	responsesReasoningFrameSize   = len(responsesReasoningFrameMagic) + responsesReasoningLengthSize
	responsesReasoningSummary     = "summary"
	responsesReasoningContent     = "content"
	responsesReasoningSummaryText = "summary_text"
	responsesReasoningContentText = "reasoning_text"
)

var responsesReasoningFrameMagic = [4]byte{'O', 'A', 'R', 'I'}

type responsesReasoningSegment struct {
	ItemID string `json:"item_id"`
	Kind   string `json:"kind"`
	Index  int64  `json:"index"`
}

func (r responsesReasoningSegment) part(output int64) responsesStreamPart {
	return responsesStreamPart{output: output, kind: r.Kind, index: r.Index}
}

func (r responsesReasoningSegment) validate() error {
	if r.ItemID == "" || r.Index < 0 {
		return errors.New("reasoning segment requires an item ID and a nonnegative index")
	}
	if r.Kind != responsesReasoningSummary && r.Kind != responsesReasoningContent {
		return fmt.Errorf("unsupported reasoning segment kind %q", r.Kind)
	}
	return nil
}

// Segment frames identify streamed text. One completed item frame owns all
// remaining native fields, including segment structure, without text copies.
type responsesReasoningState struct {
	Segment *responsesReasoningSegment `json:"segment,omitzero"`
	Item    map[string]json.RawMessage `json:"item,omitzero"`
}

func (r responsesReasoningState) itemID() (string, error) {
	if r.Segment != nil {
		return r.Segment.ItemID, nil
	}
	var id string
	if err := jsonv2.Unmarshal(r.Item["id"], &id); err != nil {
		return "", fmt.Errorf("reasoning item ID: %w", err)
	}
	if id == "" {
		return "", errors.New("reasoning item ID is required")
	}
	return id, nil
}

func (r responsesReasoningState) validate() error {
	if (r.Segment != nil) == (r.Item != nil) {
		return errors.New("reasoning state requires exactly one segment or completed item")
	}
	if r.Segment != nil {
		return r.Segment.validate()
	}
	if _, err := r.itemID(); err != nil {
		return err
	}
	var kind string
	if err := jsonv2.Unmarshal(r.Item["type"], &kind); err != nil {
		return err
	}
	if kind != responsesItemTypeReasoning {
		return fmt.Errorf("reasoning item has type %q", kind)
	}
	for _, field := range []string{responsesReasoningSummary, responsesReasoningContent} {
		children, err := responsesReasoningChildren(r.Item, field)
		if err != nil {
			return err
		}
		for index := range children {
			if _, exists := children[index]["text"]; exists {
				return errors.New("reasoning state must not contain Core-owned text")
			}
		}
	}
	return nil
}

func responsesReasoningChildren(item map[string]json.RawMessage, field string) ([]map[string]json.RawMessage, error) {
	raw, exists := item[field]
	if !exists && field == responsesReasoningContent {
		return nil, nil
	}
	if !presentJSON(raw) {
		return nil, fmt.Errorf("reasoning %s must be an array", field)
	}
	var children []map[string]json.RawMessage
	if err := jsonv2.Unmarshal(raw, &children); err != nil {
		return nil, fmt.Errorf("reasoning %s: %w", field, err)
	}
	wantKind := responsesReasoningSummaryText
	if field == responsesReasoningContent {
		wantKind = responsesReasoningContentText
	}
	for index := range children {
		var kind string
		if err := jsonv2.Unmarshal(children[index]["type"], &kind); err != nil {
			return nil, fmt.Errorf("reasoning %s[%d].type: %w", field, index, err)
		}
		if kind != wantKind {
			return nil, fmt.Errorf("reasoning %s[%d] has type %q", field, index, kind)
		}
	}
	return children, nil
}

func completedResponsesReasoningState(item responses.ResponseReasoningItem) (responsesReasoningState, error) {
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal([]byte(item.RawJSON()), &fields); err != nil {
		return responsesReasoningState{}, err
	}
	for _, field := range []string{responsesReasoningSummary, responsesReasoningContent} {
		children, err := responsesReasoningChildren(fields, field)
		if err != nil {
			return responsesReasoningState{}, err
		}
		if _, exists := fields[field]; !exists {
			continue
		}
		for index := range children {
			var text *string
			if err = jsonv2.Unmarshal(children[index]["text"], &text); err != nil {
				return responsesReasoningState{}, err
			}
			if text == nil {
				return responsesReasoningState{}, fmt.Errorf("reasoning %s[%d].text is required", field, index)
			}
			delete(children[index], "text")
		}
		fields[field], err = jsonv2.Marshal(children)
		if err != nil {
			return responsesReasoningState{}, err
		}
	}
	return responsesReasoningState{Item: fields}, nil
}

type responsesReasoningFrame struct {
	Issuer string                  `json:"issuer"`
	State  responsesReasoningState `json:"state"`
}

func (r responsesReasoningFrame) validate() error {
	if err := validateProvider(r.Issuer); err != nil {
		return fmt.Errorf("reasoning issuer: %w", err)
	}
	return r.State.validate()
}

// The binding supplies the issuer. A frame carries that immutable origin with
// the native state, so shared wire syntax cannot authorize cross-provider replay.
type responsesReasoningCodec struct {
	provider string
}

func (r responsesReasoningCodec) encodeFrame(state responsesReasoningState) ([]byte, error) {
	payload := responsesReasoningFrame{Issuer: r.provider, State: state}
	if err := payload.validate(); err != nil {
		return nil, err
	}
	raw, err := jsonv2.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if uint64(len(raw)) > uint64(^uint32(0)) {
		return nil, errors.New("reasoning item exceeds framing limit")
	}
	frame := make([]byte, responsesReasoningFrameSize+len(raw))
	copy(frame, responsesReasoningFrameMagic[:])
	binary.BigEndian.PutUint32(frame[len(responsesReasoningFrameMagic):responsesReasoningFrameSize], uint32(len(raw)))
	copy(frame[responsesReasoningFrameSize:], raw)
	return frame, nil
}

func (r responsesReasoningCodec) decodeFrames(encoded []byte) ([]responsesReasoningState, error) {
	if len(encoded) < len(responsesReasoningFrameMagic) || !bytes.Equal(encoded[:len(responsesReasoningFrameMagic)], responsesReasoningFrameMagic[:]) {
		return nil, nil
	}
	var states []responsesReasoningState
	ownIssuer := true
	for offset := 0; offset < len(encoded); {
		if len(encoded)-offset < responsesReasoningFrameSize {
			return nil, errors.New("truncated frame header")
		}
		if !bytes.Equal(encoded[offset:offset+len(responsesReasoningFrameMagic)], responsesReasoningFrameMagic[:]) {
			return nil, fmt.Errorf("invalid frame magic at byte %d", offset)
		}
		length := binary.BigEndian.Uint32(encoded[offset+len(responsesReasoningFrameMagic) : offset+responsesReasoningFrameSize])
		offset += responsesReasoningFrameSize
		if uint64(length) > uint64(len(encoded)-offset) {
			return nil, fmt.Errorf("frame length %d exceeds remaining %d bytes", length, len(encoded)-offset)
		}
		var frame *responsesReasoningFrame
		if err := jsonv2.Unmarshal(encoded[offset:offset+int(length)], &frame, jsonv2.RejectUnknownMembers(true)); err != nil {
			return nil, fmt.Errorf("decode reasoning frame: %w", err)
		}
		if frame == nil {
			return nil, errors.New("reasoning frame must be an object")
		}
		if err := frame.validate(); err != nil {
			return nil, err
		}
		if frame.Issuer != r.provider {
			ownIssuer = false
		}
		states = append(states, frame.State)
		offset += int(length)
	}
	if !ownIssuer {
		return nil, nil
	}
	return states, nil
}

type responsesReasoningReplay struct {
	itemID string
	item   map[string]json.RawMessage
	texts  map[responsesReasoningSegment]string
}

func (r *responsesReasoningReplay) add(part corechat.Part, states []responsesReasoningState) error {
	var segment *responsesReasoningSegment
	for _, state := range states {
		id, err := state.itemID()
		if err != nil {
			return err
		}
		if id != r.itemID {
			return errors.New("reasoning part contains multiple item identities")
		}
		if state.Segment != nil {
			if segment != nil && *segment != *state.Segment {
				return errors.New("reasoning part contains multiple segment identities")
			}
			segment = state.Segment
		} else {
			if r.item != nil {
				return errors.New("reasoning item contains multiple completed states")
			}
			r.item = state.Item
		}
	}
	if segment == nil {
		if part.Text != "" {
			return errors.New("reasoning item state has no text segment")
		}
		return nil
	}
	if r.texts == nil {
		r.texts = make(map[responsesReasoningSegment]string)
	}
	r.texts[*segment] += part.Text
	return nil
}

func (r *responsesReasoningReplay) param() (responses.ResponseReasoningItemParam, error) {
	if r.item == nil {
		return responses.ResponseReasoningItemParam{}, errors.New("reasoning replay requires the completed native item state")
	}
	fields := maps.Clone(r.item)
	segments := 0
	for _, field := range []string{responsesReasoningSummary, responsesReasoningContent} {
		children, err := responsesReasoningChildren(fields, field)
		if err != nil {
			return responses.ResponseReasoningItemParam{}, err
		}
		if _, exists := fields[field]; !exists {
			continue
		}
		for index := range children {
			segment := responsesReasoningSegment{ItemID: r.itemID, Kind: field, Index: int64(index)}
			text, exists := r.texts[segment]
			if !exists {
				return responses.ResponseReasoningItemParam{}, fmt.Errorf("reasoning %s[%d] is missing its Core text part", field, index)
			}
			children[index]["text"], err = jsonv2.Marshal(text)
			if err != nil {
				return responses.ResponseReasoningItemParam{}, err
			}
			segments++
		}
		fields[field], err = jsonv2.Marshal(children)
		if err != nil {
			return responses.ResponseReasoningItemParam{}, err
		}
	}
	if segments != len(r.texts) {
		return responses.ResponseReasoningItemParam{}, errors.New("reasoning text parts do not match the completed native item")
	}
	raw, err := jsonv2.Marshal(fields)
	if err != nil {
		return responses.ResponseReasoningItemParam{}, err
	}
	return param.Override[responses.ResponseReasoningItemParam](json.RawMessage(raw)), nil
}
