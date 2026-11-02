package interaction

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func FuzzOutputJSON(f *testing.F) {
	message := chat.NewAssistantMessage(chat.NewTextPart("done"))
	for _, output := range []Output{
		{
			ModelCalls:    2,
			ModelResponse: &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}},
		},
		{
			ModelCalls: 1,
			DirectToolResults: []chat.ToolResult{
				{ID: "first", Name: "direct", Output: toolcontract.NewTextOutput("done")},
				{ID: "last", Name: "direct", Output: toolcontract.Output{Details: json.RawMessage(`{"id":9007199254740993}`)}},
			},
		},
	} {
		encoded, err := run.EncodePayload(output)
		if err != nil {
			f.Fatal(err)
		}
		seed, err := encoded.Decode[Output]()
		if err != nil {
			f.Fatalf("invalid Output seed: %v", err)
		}
		if err := seed.Validate(); err != nil {
			f.Fatal(err)
		}
		f.Add([]byte(encoded.JSON()))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"model_calls":0}`))
	f.Add([]byte(`{"model_calls":1,"direct_tool_results":[]}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		erased, err := run.ParsePayload(payload)
		if err != nil {
			return
		}
		output, err := erased.Decode[Output]()
		if err != nil || output.Validate() != nil {
			return
		}
		encoded, err := run.EncodePayload(output)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := encoded.Decode[Output]()
		if err != nil {
			t.Fatalf("accepted Output did not round trip: %v", err)
		}
		if validationErr := restored.Validate(); validationErr != nil {
			t.Fatalf("restored Output is invalid: %v", validationErr)
		}
		reencoded, err := run.EncodePayload(restored)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded.JSON(), reencoded.JSON()) {
			t.Fatalf("Output encoding changed after restoration\nfirst: %s\nsecond: %s", encoded.JSON(), reencoded.JSON())
		}
	})
}
