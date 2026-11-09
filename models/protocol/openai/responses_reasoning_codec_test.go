package openai

import (
	"encoding/json"
	"testing"
)

func TestResponsesReasoningCodecOmitsMixedIssuers(t *testing.T) {
	state := responsesReasoningState{Item: map[string]json.RawMessage{
		"id": json.RawMessage(`"reasoning"`), "type": json.RawMessage(`"reasoning"`), "summary": json.RawMessage(`[]`),
	}}
	codec := responsesReasoningCodec{provider: ProviderID}
	own, err := codec.encodeFrame(state)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := (responsesReasoningCodec{provider: "perplexity"}).encodeFrame(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, frames := range [][][]byte{{own, foreign}, {foreign, own}} {
		encoded := append(append([]byte(nil), frames[0]...), frames[1]...)
		decoded, err := codec.decodeFrames(encoded)
		if err != nil || len(decoded) != 0 {
			t.Fatalf("mixed issuer state = %#v, %v; want no replay", decoded, err)
		}
	}
}
