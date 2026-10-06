package protocol

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestToolReplayPreservesCurrentCoreIdentity(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, nativeID := range []string{"", provider + "/generated/native", "native-call"} {
			t.Run(provider+"/"+nativeID, func(t *testing.T) {
				response, err := aggregateProtocolResponse(t, provider, &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
					Content: &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
						ID: nativeID, Name: "lookup", Args: map[string]any{"key": "value"},
					}}}}, FinishReason: genai.FinishReasonStop,
				}}})
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
				for _, id := range []string{restored.Parts[0].ToolCall.ID, provider + "/generated/current"} {
					restored.Parts[0].ToolCall.ID = id
					_, contents, err := mapProtocolMessages(provider, []corechat.Message{
						restored,
						corechat.NewToolMessage(corechat.ToolResult{ID: id, Name: "lookup", Output: corechat.NewTextToolOutput("found")}),
					})
					if err != nil {
						t.Fatal(err)
					}
					call := contents[0].Parts[0].FunctionCall
					result := contents[1].Parts[0].FunctionResponse
					if call.ID != id || result.ID != id {
						t.Fatalf("replayed call/result IDs = %q/%q, want %q", call.ID, result.ID, id)
					}
				}
			})
		}
	}
}
