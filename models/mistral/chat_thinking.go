package mistral

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
)

const (
	thinkingFrameMagic      = "MSTH"
	thinkingFrameLengthSize = 4
	thinkingFrameHeaderSize = len(thinkingFrameMagic) + thinkingFrameLengthSize
)

func encodeThinkingFrame(raw json.RawMessage) ([]byte, error) {
	if uint64(len(raw)) > uint64(^uint32(0)) {
		return nil, errors.New("mistral: thinking chunk exceeds framing limit")
	}
	frame := make([]byte, thinkingFrameHeaderSize+len(raw))
	copy(frame, thinkingFrameMagic)
	binary.BigEndian.PutUint32(frame[len(thinkingFrameMagic):thinkingFrameHeaderSize], uint32(len(raw)))
	copy(frame[thinkingFrameHeaderSize:], raw)
	return frame, nil
}

func decodeThinkingFrames(signature []byte) ([]json.RawMessage, bool, error) {
	if len(signature) < len(thinkingFrameMagic) || string(signature[:len(thinkingFrameMagic)]) != thinkingFrameMagic {
		return nil, false, nil
	}
	var frames []json.RawMessage
	for offset := 0; offset < len(signature); {
		if len(signature)-offset < thinkingFrameHeaderSize {
			return nil, true, errors.New("truncated thinking frame header")
		}
		if string(signature[offset:offset+len(thinkingFrameMagic)]) != thinkingFrameMagic {
			return nil, true, fmt.Errorf("invalid thinking frame magic at byte %d", offset)
		}
		length := int(binary.BigEndian.Uint32(signature[offset+len(thinkingFrameMagic) : offset+thinkingFrameHeaderSize]))
		offset += thinkingFrameHeaderSize
		if length > len(signature)-offset {
			return nil, true, fmt.Errorf("thinking frame length %d exceeds remaining %d bytes", length, len(signature)-offset)
		}
		raw := json.RawMessage(bytes.Clone(signature[offset : offset+length]))
		if !jsontext.Value(raw).IsValid() {
			return nil, true, errors.New("thinking frame contains invalid JSON")
		}
		frames = append(frames, raw)
		offset += length
	}
	return frames, true, nil
}

func coalesceThinkingFrames(frames []json.RawMessage) (json.RawMessage, error) {
	if len(frames) == 0 {
		return nil, nil
	}
	type wireThinking struct {
		Type     contentType       `json:"type"`
		Thinking []json.RawMessage `json:"thinking"`
		Closed   *bool             `json:"closed,omitzero"`
	}
	merged := wireThinking{Type: contentTypeThinking}
	for index := range frames {
		var chunk wireThinking
		if err := jsonv2.Unmarshal(frames[index], &chunk); err != nil {
			return nil, fmt.Errorf("thinking frame %d: %w", index, err)
		}
		if chunk.Type != contentTypeThinking {
			return nil, fmt.Errorf("thinking frame %d has type %q", index, chunk.Type)
		}
		merged.Thinking = append(merged.Thinking, chunk.Thinking...)
		if chunk.Closed != nil {
			closed := *chunk.Closed
			merged.Closed = &closed
		}
	}
	return jsonv2.Marshal(merged)
}
