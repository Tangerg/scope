package interaction_test

import (
	"bytes"
	"encoding/json"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func FuzzOutputJSON(f *testing.F) {
	message := chat.NewAssistantMessage(chat.NewTextPart("done"))
	for _, output := range []interaction.Output{
		{
			Source: interaction.CompletionSourceModelResponse, ModelCalls: 2,
			ModelResponse: &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}},
		},
		{
			Source: interaction.CompletionSourceDirectToolResults, ModelCalls: 1,
			DirectToolResults: []chat.ToolResult{
				{ID: "first", Name: "direct", Output: chat.NewTextToolOutput("done")},
				{ID: "last", Name: "direct", Output: chat.ToolOutput{Details: json.RawMessage(`{"id":9007199254740993}`)}},
			},
		},
	} {
		encoded, err := agent.EncodeOutput(output)
		if err != nil {
			f.Fatal(err)
		}
		seed, err := encoded.Decode[interaction.Output]()
		if err != nil {
			f.Fatalf("invalid Output seed: %v", err)
		}
		if err := seed.Validate(); err != nil {
			f.Fatal(err)
		}
		f.Add([]byte(encoded.JSON()))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"source":"model_response","model_calls":0}`))
	f.Add([]byte(`{"source":"direct_tool_results","model_calls":1,"direct_tool_results":[]}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		erased, err := agent.ParseOutput(payload)
		if err != nil {
			return
		}
		output, err := erased.Decode[interaction.Output]()
		if err != nil || output.Validate() != nil {
			return
		}
		encoded, err := agent.EncodeOutput(output)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := encoded.Decode[interaction.Output]()
		if err != nil {
			t.Fatalf("accepted Output did not round trip: %v", err)
		}
		if validationErr := restored.Validate(); validationErr != nil {
			t.Fatalf("restored Output is invalid: %v", validationErr)
		}
		reencoded, err := agent.EncodeOutput(restored)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded.JSON(), reencoded.JSON()) {
			t.Fatalf("Output encoding changed after restoration\nfirst: %s\nsecond: %s", encoded.JSON(), reencoded.JSON())
		}
	})
}
