package interaction

import (
	"bytes"
	"encoding/json"
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
			Operation: operationModelCall,
			ModelCall: &modelCall{
				ModelCallSequence: 1,
				Request: chat.Request{Messages: []chat.Message{
					chat.NewUserMessage(chat.NewTextPart("hello")),
				}},
				AdvertisedToolNames: []string{"ask"}, AppliedSteerSignalIDs: []agent.SignalID{signalID},
			},
		},
		{Operation: operationToolCall, ToolCall: &toolCall{ModelCallSequence: 1, Call: call}},
		{
			Operation: operationToolCall,
			ToolCall: &toolCall{
				ModelCallSequence: 1, ToolCallIndex: 2, Call: call,
				Checkpoint: fuzzToolCheckpoint(), InputResponse: json.RawMessage(`"Ada"`),
			},
		},
	} {
		encoded, err := encodeProtocol(effect)
		if err != nil {
			f.Fatal(err)
		}
		if _, err := decodeEffect(encoded); err != nil {
			f.Fatalf("invalid effect seed: %v", err)
		}
		f.Add([]byte(encoded))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"operation":"tool_call","tool_call":{"model_call_sequence":1,"call":{"id":"call","name":"ask"},"input_response":"Ada"}}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		effect, err := decodeEffect(payload)
		if err != nil {
			return
		}
		encoded, err := encodeProtocol(effect)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := decodeEffect(encoded)
		if err != nil {
			t.Fatalf("accepted effect did not round trip: %v", err)
		}
		reencoded, err := encodeProtocol(restored)
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
	result := &chat.ToolResult{ID: "call", Name: "ask", Output: chat.NewTextToolOutput("Ada")}
	failed := &chat.ToolResult{ID: "call", Name: "ask", IsError: true, Output: chat.NewTextToolOutput("refused")}
	checkpoint := fuzzToolCheckpoint()
	for _, signal := range []signalEnvelope{
		{Operation: operationModelCall, ModelResult: &modelCallResult{Response: response}},
		{Operation: operationModelCall, ModelResult: &modelCallResult{
			Response: response, ReplacementMessages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("summary"))},
		}},
		{Operation: operationModelCall, ModelResult: &modelCallResult{Error: "provider unavailable"}},
		{Operation: operationModelCall, ModelResult: &modelCallResult{HostError: "request preparation failed"}},
		{Operation: operationToolCall, ToolResult: &toolCallResult{Result: result, AdvertisedToolNames: []string{"ask"}}},
		{Operation: operationToolCall, ToolResult: &toolCallResult{Result: result, Direct: true}},
		{Operation: operationToolCall, ToolResult: &toolCallResult{Result: failed}},
		{Operation: operationToolCall, ToolResult: &toolCallResult{Checkpoint: checkpoint}},
		{Operation: operationWaitOpened, WaitOpened: &checkpoint.InputRequest},
		{Operation: operationInputResponse, InputResponse: json.RawMessage(`{"answer":9007199254740993}`)},
		{Operation: operationSteer, Steer: &steerInput{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("continue"))}}},
	} {
		encoded, err := encodeProtocol(signal)
		if err != nil {
			f.Fatal(err)
		}
		if _, err := decodeSignal(encoded); err != nil {
			f.Fatalf("invalid signal seed: %v", err)
		}
		f.Add([]byte(encoded))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"operation":"model_call","model_result":{"error":"provider","host_error":"host"}}`))
	f.Add([]byte(`{"operation":"input_response","input_response":{},"steer":{"messages":[]}}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		signal, err := decodeSignal(payload)
		if err != nil {
			return
		}
		encoded, err := encodeProtocol(signal)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := decodeSignal(encoded)
		if err != nil {
			t.Fatalf("accepted signal did not round trip: %v", err)
		}
		reencoded, err := encodeProtocol(restored)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, reencoded) {
			t.Fatalf("signal encoding changed after restoration\nfirst: %s\nsecond: %s", encoded, reencoded)
		}
	})
}

func fuzzToolCheckpoint() *toolCheckpoint {
	return &toolCheckpoint{
		PauseCount: 2,
		InputRequest: inputRequestWire{
			Prompt:            json.RawMessage(`{"question":"Name?"}`),
			ResponseSchema:    json.RawMessage(`{"type":"string","minLength":1}`),
			ContinuationState: json.RawMessage(`{"stage":"name","id":9007199254740993}`),
		},
	}
}
