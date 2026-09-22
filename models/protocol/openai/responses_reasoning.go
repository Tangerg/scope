package openai

import (
	"bytes"
	"encoding/binary"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3/responses"
)

const responsesReasoningFrameSize = 8

var responsesReasoningFrameMagic = [4]byte{'O', 'A', 'R', 'I'}

func encodeResponsesReasoningFrame(item responses.ResponseReasoningItemParam) ([]byte, error) {
	raw, err := jsonv2.Marshal(item)
	if err != nil {
		return nil, err
	}
	if uint64(len(raw)) > uint64(^uint32(0)) {
		return nil, errors.New("reasoning item exceeds framing limit")
	}
	frame := make([]byte, responsesReasoningFrameSize+len(raw))
	copy(frame, responsesReasoningFrameMagic[:])
	binary.BigEndian.PutUint32(frame[4:responsesReasoningFrameSize], uint32(len(raw)))
	copy(frame[responsesReasoningFrameSize:], raw)
	return frame, nil
}

func decodeResponsesReasoningFrames(signature []byte) ([]responses.ResponseReasoningItemParam, bool, error) {
	if len(signature) < len(responsesReasoningFrameMagic) || !bytes.Equal(signature[:len(responsesReasoningFrameMagic)], responsesReasoningFrameMagic[:]) {
		return nil, false, nil
	}
	var items []responses.ResponseReasoningItemParam
	for offset := 0; offset < len(signature); {
		if len(signature)-offset < responsesReasoningFrameSize {
			return nil, true, errors.New("truncated frame header")
		}
		if !bytes.Equal(signature[offset:offset+4], responsesReasoningFrameMagic[:]) {
			return nil, true, fmt.Errorf("invalid frame magic at byte %d", offset)
		}
		length := int(binary.BigEndian.Uint32(signature[offset+4 : offset+responsesReasoningFrameSize]))
		offset += responsesReasoningFrameSize
		if length > len(signature)-offset {
			return nil, true, fmt.Errorf("frame length %d exceeds remaining %d bytes", length, len(signature)-offset)
		}
		var item responses.ResponseReasoningItemParam
		if err := jsonv2.Unmarshal(signature[offset:offset+length], &item); err != nil {
			return nil, true, fmt.Errorf("decode reasoning item: %w", err)
		}
		if item.ID == "" {
			return nil, true, errors.New("reasoning item ID is required")
		}
		items = append(items, item)
		offset += length
	}
	return items, true, nil
}
