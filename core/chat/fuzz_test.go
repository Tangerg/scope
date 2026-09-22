package chat_test

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

type protocolValue interface {
	Validate() error
}

func assertJSONFixedPoint[T any](t *testing.T, data []byte) {
	t.Helper()
	var first T
	if err := jsonv2.Unmarshal(data, &first); err != nil {
		return
	}
	validator, ok := any(&first).(protocolValue)
	if !ok {
		t.Fatalf("%T does not implement Validate", &first)
	}
	if err := validator.Validate(); err != nil {
		t.Fatalf("successful Unmarshal produced invalid %T: %v", first, err)
	}
	firstWire, err := jsonv2.Marshal(first)
	if err != nil {
		t.Fatalf("Marshal after successful Unmarshal: %v", err)
	}

	var second T
	if unmarshalErr := jsonv2.Unmarshal(firstWire, &second); unmarshalErr != nil {
		t.Fatalf("Unmarshal canonical wire: %v", unmarshalErr)
	}
	secondWire, err := jsonv2.Marshal(second)
	if err != nil {
		t.Fatalf("Marshal second value: %v", err)
	}
	if !bytes.Equal(firstWire, secondWire) {
		t.Fatalf("wire did not reach fixed point: first=%s second=%s", firstWire, secondWire)
	}
}

func FuzzPartJSON(f *testing.F) {
	for _, seed := range []string{
		`{"kind":"text","text":"hello"}`,
		`{"kind":"media","media":{"mime":"image/png","source":{"kind":"bytes","bytes":"AQID"}}}`,
		`{"kind":"reasoning","text":"thinking","signature":"c2ln"}`,
		`{"kind":"tool_call","tool_call":{"id":"call","name":"tool","arguments":"{\"x\":1}"}}`,
		`{"kind":"tool_call_delta","tool_call_delta":{"id":"call","name":"tool","arguments":"{"}}`,
		`{"kind":"tool_result","tool_result":{"id":"call","name":"tool","output":{"content":[{"kind":"text","text":"ok"}]}}}`,
		`{"kind":"future","text":"unknown"}`,
		`{}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) { assertJSONFixedPoint[chat.Part](t, data) })
}

func FuzzMessageJSON(f *testing.F) {
	for _, seed := range []string{
		`{"role":"system","parts":[{"kind":"text","text":"rules"}]}`,
		`{"role":"user","parts":[{"kind":"text","text":"hello"}]}`,
		`{"role":"assistant","parts":[{"kind":"reasoning","text":"thinking"},{"kind":"text","text":"answer"}]}`,
		`{"role":"tool","parts":[{"kind":"tool_result","tool_result":{"id":"call","name":"tool","output":{"content":[{"kind":"text","text":"ok"}]}}}]}`,
		`{"role":"future","parts":[{"kind":"text","text":"unknown"}]}`,
		`{"role":"user","parts":[]}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) { assertJSONFixedPoint[chat.Message](t, data) })
}

func FuzzRequestJSON(f *testing.F) {
	for _, seed := range []string{
		`{"messages":[{"role":"user","parts":[{"kind":"text","text":"hello"}]}]}`,
		`{"messages":[{"role":"user","parts":[{"kind":"text","text":"hello"}]}],"options":{"extensions":{"openai/key":{"enabled":true}}}}`,
		`{"messages":[]}`,
		`{}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) { assertJSONFixedPoint[chat.Request](t, data) })
}

func FuzzResponseJSON(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"output":{"message":{"role":"assistant","parts":[{"kind":"text","text":"hello"}]},"finish_reason":"stop"}}`,
		`{"output":{"finish_reason":"stop"},"metadata":{"usage":{"input_tokens":1,"output_tokens":2}}}`,
		`{"output":{"finish_reason":"future"}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) { assertJSONFixedPoint[chat.Response](t, data) })
}
