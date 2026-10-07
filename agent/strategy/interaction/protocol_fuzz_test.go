package interaction

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func FuzzInteractionEffectProtocol(f *testing.F) {
	signalID, err := agent.ParseSignalID("signal:steer")
	if err != nil {
		f.Fatal(err)
	}
	call := chat.ToolCall{ID: "call", Name: "ask", Arguments: `{}`}
	for _, effect := range []effectEnvelope{
		{
			ModelCall: &modelCall{
				ModelCallSequence: 1,
				Request: chat.Request{Messages: []chat.Message{
					chat.NewUserMessage(chat.NewTextPart("hello")),
				}},
				AdvertisedToolNames: []string{"ask"}, AppliedSteerSignalIDs: []agent.SignalID{signalID},
			},
		},
		{ToolCall: &toolDispatchRequest{Invocation: toolCall{ModelCallSequence: 1, Call: call}}},
		{
			ToolCall: &toolDispatchRequest{
				Invocation: toolCall{ModelCallSequence: 1, ToolCallIndex: 2, Call: call},
				Resume:     &toolResume{InputRequest: fuzzToolCheckpoint(f).InputRequest, InputResponse: json.RawMessage(`"Ada"`)},
			},
		},
	} {
		encoded, err := jsonv2.Marshal(effect, jsonv2.Deterministic(true))
		if err != nil {
			f.Fatal(err)
		}
		if _, err := decodeEffect(encoded); err != nil {
			f.Fatalf("invalid effect seed: %v", err)
		}
		f.Add([]byte(encoded))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"tool_call":{"model_call_sequence":1,"call":{"id":"call","name":"ask"},"input_response":"Ada"}}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		effect, err := decodeEffect(payload)
		if err != nil {
			return
		}
		encoded, err := jsonv2.Marshal(effect, jsonv2.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		restored, err := decodeEffect(encoded)
		if err != nil {
			t.Fatalf("accepted effect did not round trip: %v", err)
		}
		reencoded, err := jsonv2.Marshal(restored, jsonv2.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, reencoded) {
			t.Fatalf("effect encoding changed after restoration\nfirst: %s\nsecond: %s", encoded, reencoded)
		}
	})
}

func FuzzInteractionSignalProtocol(f *testing.F) {
	message := chat.NewAssistantMessage(chat.NewTextPart("done"))
	response := &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}
	result := chat.ToolResult{ID: "call", Name: "ask", Output: chat.NewTextToolOutput("Ada")}
	failed := chat.ToolResult{ID: "call", Name: "ask", IsError: true, Output: chat.NewTextToolOutput("refused")}
	checkpoint := fuzzToolCheckpoint(f)
	for _, signal := range []signalEnvelope{
		{ModelResult: &modelCallResult{Response: response}},
		{ModelResult: &modelCallResult{
			Response: response, ReplacementMessages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("summary"))},
		}},
		{ToolResult: &toolDispatchResult{Completion: &toolCallResult{Disposition: ResultSucceeded, Output: result.Output, AdvertisedToolNames: []string{"ask"}}}},
		{ToolResult: &toolDispatchResult{Completion: &toolCallResult{Disposition: ResultFailed, Output: failed.Output}}},
		{ToolResult: &toolDispatchResult{InputRequest: &checkpoint.InputRequest}},
		{InputResponse: json.RawMessage(`{"answer":9007199254740993}`)},
		{Steer: &steerInput{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("continue"))}}},
	} {
		encoded, err := jsonv2.Marshal(signal, jsonv2.Deterministic(true))
		if err != nil {
			f.Fatal(err)
		}
		if _, err := decodeSignal(encoded); err != nil {
			f.Fatalf("invalid signal seed: %v", err)
		}
		f.Add([]byte(encoded))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"model_result":{"error":"provider","host_error":"host"}}`))
	f.Add([]byte(`{"input_response":{},"steer":{"messages":[]}}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		signal, err := decodeSignal(payload)
		if err != nil {
			return
		}
		encoded, err := jsonv2.Marshal(signal, jsonv2.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		restored, err := decodeSignal(encoded)
		if err != nil {
			t.Fatalf("accepted signal did not round trip: %v", err)
		}
		reencoded, err := jsonv2.Marshal(restored, jsonv2.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, reencoded) {
			t.Fatalf("signal encoding changed after restoration\nfirst: %s\nsecond: %s", encoded, reencoded)
		}
	})
}

func fuzzToolCheckpoint(f *testing.F) *toolCheckpoint {
	f.Helper()
	request, err := newToolInputRequest(
		json.RawMessage(`{"question":"Name?"}`),
		json.RawMessage(`{"type":"string","minLength":1}`),
		json.RawMessage(`{"stage":"name","id":9007199254740993}`),
	)
	if err != nil {
		f.Fatal(err)
	}
	return &toolCheckpoint{PauseCount: 2, InputRequest: request}
}

func TestInputResponseRequiresPresentJSON(t *testing.T) {
	if _, err := decodeSignal([]byte(`{}`)); err == nil {
		t.Fatal("accepted a missing answer")
	}
	for _, raw := range []string{`null`, `""`, `{}`, `[]`, `false`, `0`} {
		signal, err := decodeSignal([]byte(`{"input_response":` + raw + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if string(signal.InputResponse) != raw {
			t.Fatalf("answer = %s, want %s", signal.InputResponse, raw)
		}
	}
}
