package ollama

import (
	"encoding/json"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

func TestToolCompletionPreservesProviderOutcome(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   corechat.FinishReason
	}{
		{"stop", corechat.FinishReasonToolCalls},
		{"", corechat.FinishReasonToolCalls},
		{"length", corechat.FinishReasonLength},
		{"other", corechat.FinishReasonOther},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			var native nativeChatResponse
			if err := json.Unmarshal([]byte(`{"model":"test","message":{"role":"assistant","tool_calls":[{"function":{"name":"inspect","arguments":{}}}]},"done":true}`), &native); err != nil {
				t.Fatal(err)
			}
			native.DoneReason = tc.reason
			response, err := newProtocolResponseMapper().mapResponse("test", native)
			if err != nil {
				t.Fatal(err)
			}
			if response.Output.FinishReason != tc.want {
				t.Fatalf("Call finish = %s, want %s", response.Output.FinishReason, tc.want)
			}
			mapper := newProtocolResponseMapper()
			native.Done = false
			native.DoneReason = ""
			first, err := mapper.mapDelta("test", native)
			if err != nil {
				t.Fatal(err)
			}
			if first.FinishReason != "" {
				t.Fatal("unfinished tool chunk received a finish reason")
			}
			native.Message = nativeMessage{}
			native.Done = true
			native.DoneReason = tc.reason
			terminal, err := mapper.mapDelta("test", native)
			if err != nil {
				t.Fatal(err)
			}
			var accumulator corechat.ResponseAccumulator
			for _, delta := range []*corechat.ResponseDelta{first, terminal} {
				if addErr := accumulator.Add(delta); addErr != nil {
					t.Fatal(addErr)
				}
			}
			streamed, err := accumulator.Response()
			if err != nil {
				t.Fatal(err)
			}
			if streamed.Output.FinishReason != tc.want {
				t.Fatalf("Stream finish = %s, want %s", streamed.Output.FinishReason, tc.want)
			}
			if tc.reason != "" {
				for _, output := range []*corechat.Output{response.Output, streamed.Output} {
					reason, found, err := output.Metadata.Extra.Decode[string](protocolNativeDoneReasonKey)
					if err != nil || !found || reason != tc.reason {
						t.Fatalf("native reason = %s, found %v, error %v", reason, found, err)
					}
				}
			}
		})
	}
}
