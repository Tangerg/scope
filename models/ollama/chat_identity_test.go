package ollama

import (
	jsonv2 "encoding/json/v2"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestToolReplayPreservesCurrentCoreIdentity(t *testing.T) {
	for _, nativeID := range []string{"", "ollama/generated/native", "native-call"} {
		t.Run(nativeID, func(t *testing.T) {
			mapper := newProtocolResponseMapper()
			delta, err := mapper.mapDelta("model", nativeChatResponse{Done: true, Message: nativeMessage{
				ToolCalls: []nativeToolCall{{ID: nativeID, Function: nativeToolCallFunction{
					Name: "lookup", Arguments: emptyNativeJSONObject(),
				}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			var accumulator corechat.ResponseAccumulator
			if addErr := accumulator.Add(delta); addErr != nil {
				t.Fatal(addErr)
			}
			response, err := accumulator.Response()
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := jsonv2.Marshal(response.Output.Message)
			if err != nil {
				t.Fatal(err)
			}
			var restored corechat.Message
			if err := jsonv2.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{restored.Parts[0].ToolCall.ID, "ollama/generated/current"} {
				restored.Parts[0].ToolCall.ID = id
				messages, err := mapProtocolMessages([]corechat.Message{
					restored,
					corechat.NewToolMessage(corechat.ToolResult{ID: id, Name: "lookup", Output: corechat.NewTextToolOutput("found")}),
				})
				if err != nil {
					t.Fatal(err)
				}
				call, result := messages[0].ToolCalls[0], messages[1]
				if call.ID != id || result.ToolCallID != id {
					t.Fatalf("replayed call/result IDs = %q/%q, want %q", call.ID, result.ToolCallID, id)
				}
			}
		})
	}
}
