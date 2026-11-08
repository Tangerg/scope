package openai

import (
	"encoding/binary"
	"testing"
)

func TestReasoningFrameLengthCannotNarrowBeforeAdmission(t *testing.T) {
	for _, length := range []uint32{1 << 31, ^uint32(0)} {
		responsesFrame := make([]byte, responsesReasoningFrameSize)
		copy(responsesFrame, responsesReasoningFrameMagic[:])
		binary.BigEndian.PutUint32(responsesFrame[len(responsesReasoningFrameMagic):], length)
		if _, framed, err := decodeResponsesReasoningFrames(responsesFrame); !framed || err == nil {
			t.Fatalf("responses length %d: framed=%t error=%v", length, framed, err)
		}
		detailFrame := make([]byte, reasoningDetailFrameHeaderSize)
		copy(detailFrame, reasoningDetailFrameMagic[:])
		binary.BigEndian.PutUint32(detailFrame[6:], length)
		codec := reasoningDetailsCodec{provider: "provider"}
		if _, framed, _, err := codec.decodeFrames(detailFrame); !framed || err == nil {
			t.Fatalf("details length %d: framed=%t error=%v", length, framed, err)
		}
	}
}
