package chat_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func TestProtocolEncodingRejectsInvalidUTF8(t *testing.T) {
	invalid := string([]byte{0xff})
	for name, value := range map[string]any{
		"text":            chat.NewTextPart(invalid),
		"reasoning":       chat.NewReasoningPart(invalid, nil),
		"refusal":         chat.NewRefusalPart(invalid),
		"message":         chat.NewUserMessage(chat.NewTextPart(invalid)),
		"tool output":     chat.NewTextToolOutput(invalid),
		"tool arguments":  chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "tool", Arguments: invalid}),
		"media reference": &media.Media{MIME: "image/png", Source: media.Source{Kind: media.SourceReference, Ref: invalid}},
		"metadata key":    metadata.Map{invalid: json.RawMessage(`true`)},
		"metadata value":  metadata.Map{"provider/state": json.RawMessage{'"', 0xff, '"'}},
		"model name":      chat.Options{Model: invalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := jsonv2.Marshal(value); err == nil {
				t.Fatal("protocol codec silently repaired invalid UTF-8")
			}
		})
	}
}

func TestTextDeltaAdmissionRejectsInvalidUTF8Atomically(t *testing.T) {
	invalid := string([]byte{0xff})
	for name, part := range map[string]chat.PartDelta{
		"text":      chat.NewTextDelta(invalid),
		"reasoning": chat.NewReasoningDelta(invalid, nil),
		"refusal":   chat.NewRefusalDelta(invalid),
	} {
		t.Run(name, func(t *testing.T) {
			if err := part.Validate(); !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("Validate = %v, want ErrInvalidResponse", err)
			}
			var accumulator chat.ResponseAccumulator
			if err := accumulator.Add(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("你好")}}); err != nil {
				t.Fatal(err)
			}
			rejected := &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("must not append"), part}, FinishReason: chat.FinishReasonStop}
			if err := accumulator.Add(rejected); !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("Add = %v, want ErrInvalidResponse", err)
			}
			if accumulator.Text() != "你好" {
				t.Fatalf("rejected delta mutated text: %q", accumulator.Text())
			}
			accepted := &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("世界"), chat.NewReasoningDelta("考える", nil)}, FinishReason: chat.FinishReasonStop}
			if _, err := jsonv2.Marshal(accepted); err != nil {
				t.Fatal(err)
			}
			if err := accumulator.Add(accepted); err != nil {
				t.Fatal(err)
			}
			response, err := accumulator.Response()
			if err != nil || response.Output.Message.Text() != "你好世界" {
				t.Fatalf("Response = %#v, %v", response, err)
			}
		})
	}
}
