package chat_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
)

func TestResponseAccumulatorRejectsUnencodableDeltaAtomically(t *testing.T) {
	tests := []struct {
		name  string
		delta *chat.ResponseDelta
	}{
		{"identity", &chat.ResponseDelta{Metadata: &chat.ResponseMetadata{ID: "\xff"}}},
		{"model", &chat.ResponseDelta{Metadata: &chat.ResponseMetadata{Model: "\xff"}}},
		{"timestamp", &chat.ResponseDelta{Metadata: &chat.ResponseMetadata{CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}},
		{"timestamp precision", &chat.ResponseDelta{Metadata: &chat.ResponseMetadata{CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("seconds", 43))}}},
		{"tool identity", &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewToolCallDelta(chat.ToolCallDelta{ID: "\xff", Name: "tool"})}}},
		{"tool arguments", &chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewToolCallDelta(chat.ToolCallDelta{ID: "call", Name: "tool", Arguments: "\xff"})}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var accumulator chat.ResponseAccumulator
			if err := accumulator.Add(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewTextDelta("kept")}, Metadata: &chat.ResponseMetadata{ID: "original"}}); err != nil {
				t.Fatal(err)
			}
			test.delta.Parts = append(test.delta.Parts, chat.NewTextDelta("rejected"))
			test.delta.FinishReason = chat.FinishReasonStop
			if err := accumulator.Add(test.delta); !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("Add invalid delta = %v; want ErrInvalidResponse", err)
			}
			if got := accumulator.Text(); got != "kept" {
				t.Fatalf("rejected delta changed text: %q", got)
			}
			if err := accumulator.Add(&chat.ResponseDelta{FinishReason: chat.FinishReasonStop}); err != nil {
				t.Fatalf("rejected delta finished stream: %v", err)
			}
			response, err := accumulator.Response()
			if err != nil {
				t.Fatal(err)
			}
			if response.Metadata.ID != "original" || response.Metadata.Model != "" || !response.Metadata.CreatedAt.IsZero() || len(response.Output.Message.Parts) != 1 {
				t.Fatalf("rejected delta changed response: %#v", response)
			}
			if _, err := jsonv2.Marshal(response); err != nil {
				t.Fatalf("accepted stream cannot be encoded: %v", err)
			}
		})
	}
}

func TestToolProposalRetainsIncompleteJSONAndUnicode(t *testing.T) {
	for _, arguments := range []string{"", `{`, "text-值\x00"} {
		call := chat.ToolCall{ID: "call-值\x00", Name: "tool-值\x00", Arguments: arguments}
		message := chat.NewAssistantMessage(chat.NewToolCallPart(call))
		if err := message.Validate(); err != nil {
			t.Fatal(err)
		}
		encoded, err := jsonv2.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var decoded chat.Message
		if decodeErr := jsonv2.Unmarshal(encoded, &decoded); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if !reflect.DeepEqual(message, decoded) {
			t.Fatalf("tool proposal changed during round trip: %#v", decoded)
		}
		var accumulator chat.ResponseAccumulator
		if addErr := accumulator.Add(&chat.ResponseDelta{Parts: []chat.PartDelta{chat.NewToolCallDelta(chat.ToolCallDelta(call))}, FinishReason: chat.FinishReasonToolCalls}); addErr != nil {
			t.Fatal(addErr)
		}
		response, err := accumulator.Response()
		if err != nil || *response.Output.Message.Parts[0].ToolCall != call {
			t.Fatalf("proposal changed during promotion: %#v, %v", response, err)
		}
	}
}
