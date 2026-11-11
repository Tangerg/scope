package interaction

import (
	"encoding/json"
	"testing"

	"github.com/Tangerg/scope/core/tool"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
)

func FuzzInteractionEffectProtocol(f *testing.F) {
	signalID, err := run.ParseSignalID("signal:steer")
	if err != nil {
		f.Fatal(err)
	}
	call := chat.ToolCall{ID: "call", Name: "ask", Arguments: `{}`}
	for _, effect := range []effectEnvelope{
		{
			ModelCall: &modelCall{
				ModelCallSequence: 1,
				Request: Input{Messages: []chat.Message{
					chat.NewUserMessage(chat.NewTextPart("hello")),
				}},
				AppliedSteerSignalIDs: []run.SignalID{signalID},
			},
		},
		{ToolCall: &toolDispatchRequest{Invocation: toolCall{Call: call}}},
		{
			ToolCall: &toolDispatchRequest{
				Invocation: toolCall{Call: call},
				Resume:     &toolResume{Reference: testTurn(1).EffectID(0), InputRequest: fuzzToolInputRequest(f), InputResponse: json.RawMessage(`"Ada"`)},
			},
		},
	} {
		encoded, err := run.EncodePayload(effect)
		if err != nil {
			f.Fatal(err)
		}
		if _, err := decodeEffect(encoded); err != nil {
			f.Fatalf("invalid effect seed: %v", err)
		}
		f.Add([]byte(encoded.JSON()))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"tool_call":{"call":{"id":"call","name":"ask"},"input_response":"Ada"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		payload, err := run.ParsePayload(data)
		if err != nil {
			return
		}
		effect, err := decodeEffect(payload)
		if err != nil {
			return
		}
		encoded, err := run.EncodePayload(effect)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := decodeEffect(encoded)
		if err != nil {
			t.Fatalf("accepted effect did not round trip: %v", err)
		}
		reencoded, err := run.EncodePayload(restored)
		if err != nil {
			t.Fatal(err)
		}
		if !encoded.Equal(reencoded) {
			t.Fatalf("effect encoding changed after restoration\nfirst: %s\nsecond: %s", encoded.JSON(), reencoded.JSON())
		}
	})
}

func FuzzInteractionResultProtocol(f *testing.F) {
	message := chat.NewAssistantMessage(chat.NewTextPart("done"))
	response := &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}
	toolResult := chat.ToolResult{ID: "call", Name: "ask", Output: tool.NewTextOutput("Ada")}
	failed := chat.ToolResult{ID: "call", Name: "ask", IsError: true, Output: tool.NewTextOutput("refused")}
	request := fuzzToolInputRequest(f)
	for _, result := range []resultEnvelope{
		{ModelResult: &modelCallResult{Response: response}},
		{Prepared: &preparedContext{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("summary"))}}},
		{ToolResult: &toolDispatchResult{Completion: &toolCallResult{Disposition: ResultSucceeded, Output: toolResult.Output, AdvertisedToolNames: []string{"ask"}}}},
		{ToolResult: &toolDispatchResult{Completion: &toolCallResult{Disposition: ResultFailed, Output: failed.Output}}},
		{ToolResult: &toolDispatchResult{InputRequest: &request}},
	} {
		encoded, err := run.EncodePayload(result)
		if err != nil {
			f.Fatal(err)
		}
		if _, err := decodeResult(encoded); err != nil {
			f.Fatalf("invalid result seed: %v", err)
		}
		f.Add([]byte(encoded.JSON()))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"model_result":{"error":"provider","host_error":"host"}}`))
	f.Add([]byte(`{"input_response":{},"steer":{"messages":[]}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		payload, err := run.ParsePayload(data)
		if err != nil {
			return
		}
		result, err := decodeResult(payload)
		if err != nil {
			return
		}
		encoded, err := run.EncodePayload(result)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := decodeResult(encoded)
		if err != nil {
			t.Fatalf("accepted result did not round trip: %v", err)
		}
		reencoded, err := run.EncodePayload(restored)
		if err != nil {
			t.Fatal(err)
		}
		if !encoded.Equal(reencoded) {
			t.Fatalf("result encoding changed after restoration\nfirst: %s\nsecond: %s", encoded.JSON(), reencoded.JSON())
		}
	})
}

func FuzzInteractionSteeringSignal(f *testing.F) {
	id := mustValue(run.ParseSignalID("guidance"))
	seed := mustValue(NewSteerSignal(id, chat.NewUserMessage(chat.NewTextPart("continue"))))
	f.Add([]byte(seed.Payload.JSON()))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"steer":{"messages":[]}}`))
	f.Add([]byte(`{"prepared_context":{"messages":[]}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		payload, err := run.ParsePayload(raw)
		if err != nil {
			return
		}
		var batch steerBatch
		if admissionErr := batch.collectInput(run.Signal{ID: id, Payload: payload}); admissionErr != nil {
			return
		}
		encoded := mustValue(NewSteerSignal(id, batch.Messages...))
		var restored steerBatch
		if restoreErr := restored.collectInput(encoded); restoreErr != nil {
			t.Fatal("accepted external guidance did not round trip", restoreErr)
		}
		reencoded := mustValue(NewSteerSignal(id, restored.Messages...))
		if !encoded.Payload.Equal(reencoded.Payload) {
			t.Fatal("external guidance changed its canonical encoding")
		}
	})
}

func fuzzToolInputRequest(f *testing.F) tool.InputRequest {
	f.Helper()
	request, err := newInputRequest(
		json.RawMessage(`{"question":"Name?"}`),
		json.RawMessage(`{"type":"string","minLength":1}`),
		json.RawMessage(`{"stage":"name","id":9007199254740993}`),
	)
	if err != nil {
		f.Fatal(err)
	}
	return request
}

func TestInputResponseRequiresPresentJSON(t *testing.T) {
	id, err := run.ParseSignalID("signal:answer")
	if err != nil {
		t.Fatal(err)
	}
	waitKey, err := run.ParseWaitKey("wait:answer")
	if err != nil {
		t.Fatal(err)
	}
	processID, err := run.ParseProcessID("process:answer")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.Parse([]byte(`true`))
	if err != nil {
		t.Fatal(err)
	}
	question, err := run.ParsePayload(json.RawMessage(`"answer"`))
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingToolInput{processID: processID, wait: run.Wait{Key: waitKey, Question: question, AnswerSchema: schema, MaxAnswerBytes: tool.MaxInputBytes}}
	if _, err := pending.ResponseSignal(id, nil); err == nil {
		t.Fatal("accepted a missing answer")
	}
	for _, raw := range []string{`null`, `""`, `{}`, `[]`, `false`, `0`} {
		signal, err := pending.ResponseSignal(id, json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if string(signal.Payload.JSON()) != raw {
			t.Fatalf("answer = %s, want %s", signal.Payload.JSON(), raw)
		}
	}
}
