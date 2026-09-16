package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

type wireFixture struct {
	Message string `json:"message"`
}

func TestPayloadOwnsNormalizedJSON(t *testing.T) {
	source := json.RawMessage(` { "message": "hello" } `)
	input, err := ParsePayload(source)
	if err != nil {
		t.Fatal(err)
	}
	source[3] = 'x'
	if got := string(input.JSON()); got != `{"message":"hello"}` {
		t.Fatalf("Payload.JSON() = %s", got)
	}
	copyOfJSON := input.JSON()
	copyOfJSON[0] = '['
	if got := string(input.JSON()); got != `{"message":"hello"}` {
		t.Fatalf("Payload shared returned bytes: %s", got)
	}
}

func TestPayloadRejectsMalformedMultipleAndDuplicateValues(t *testing.T) {
	for _, data := range []json.RawMessage{nil, []byte(`{"message":`), []byte(`{} {}`), []byte(`{"message":"first","message":"second"}`)} {
		if _, err := ParsePayload(data); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("ParsePayload(%q) error = %v, want ErrInvalidPayload", data, err)
		}
	}
}

func TestTypedPayloadRejectsUnknownFields(t *testing.T) {
	input, err := ParsePayload([]byte(`{"message":"hello","unknown":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.Decode[wireFixture](); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("DecodePayload error = %v, want ErrInvalidPayload", err)
	}
}

func TestPayloadTypedRoundTrip(t *testing.T) {
	want := wireFixture{Message: "done"}
	output, err := EncodePayload(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := output.Decode[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Payload.Decode() = %+v, want %+v", got, want)
	}
}

func TestWireZeroValuesAreInvalid(t *testing.T) {
	if (Payload{}).Valid() {
		t.Fatal("zero wire values reported valid")
	}
	if _, err := json.Marshal(Payload{}); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("marshal Payload error = %v, want ErrInvalidPayload", err)
	}
}

func TestNormalizeJSONEnforcesNormalizedLimit(t *testing.T) {
	if _, err := normalizeJSON([]byte(`"<"`), 3); err == nil {
		t.Fatal("normalizeJSON accepted a value that exceeded the limit after normalization")
	}
}

func FuzzPayloadJSONRoundTrip(f *testing.F) {
	f.Add([]byte(`{"message":"hello"}`))
	f.Add([]byte(`[1,true,null]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		input, err := ParsePayload(data)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Payload
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if string(decoded.JSON()) != string(input.JSON()) {
			t.Fatalf("round trip = %s, want %s", decoded.JSON(), input.JSON())
		}
	})
}

// Custom codecs remain responsible for their data; the wire boundary must still
// reject malformed bytes returned by them instead of repairing their output.
type malformedWireString struct{}

func (malformedWireString) MarshalJSON() ([]byte, error) { return []byte{'"', 0xff, '"'}, nil }

func TestTypedWireRejectsInvalidUTF8BeforeEncoding(t *testing.T) {
	invalid := string([]byte{0xff})
	for name, value := range map[string]any{
		"primitive":           invalid,
		"field":               wireFixture{Message: invalid},
		"map key":             map[string]string{invalid: "value"},
		"nested":              []wireFixture{{Message: invalid}},
		"custom codec":        malformedWireString{},
		"chat text":           chat.NewUserMessage(chat.NewTextPart(invalid)),
		"chat tool arguments": chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "read", Arguments: invalid})),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := EncodePayload(value); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("input accepted invalid UTF-8: %v", err)
			}
		})
	}
	want := wireFixture{Message: "中文 🌍 \ufffd"}
	input, err := EncodePayload(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := input.Decode[wireFixture]()
	if err != nil || got != want {
		t.Fatalf("Unicode changed: %+v, %v", got, err)
	}
	for _, number := range []json.Number{"9007199254740993", "1e400", "1.234567890123456789"} {
		input, err := EncodePayload(number)
		if err != nil || string(input.JSON()) != string(number) {
			t.Fatalf("number changed: %s, %v", input.JSON(), err)
		}
	}
}

func TestTextBearingProtocolConstructorsRejectInvalidUTF8(t *testing.T) {
	invalid := string([]byte{0xff})
	if _, err := Pause(0, invalid); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Pause accepted invalid text: %v", err)
	}
	if _, err := NewDescriptor(DescriptorConfig{Name: "test", Description: invalid, InputSchema: controlValue(SchemaFor[wireFixture]()), OutputSchema: controlValue(SchemaFor[wireFixture]())}); !errors.Is(err, ErrInvalidDescriptor) {
		t.Fatalf("Descriptor accepted invalid text: %v", err)
	}
}

func TestCanonicalPayloadBytes(t *testing.T) {
	for _, test := range []struct{ source, canonical string }{
		{` {"z":1e10,"a":[-0.0,1.0,9007199254740993,1e400]} `, `{"a":[-0.0,1.0,9007199254740993,1e400],"z":1e10}`},
		{`{"text":"<>&\u2028\u2029\u0061\/"}`, `{"text":"\u003c\u003e\u0026\u2028\u2029a/"}`},
		{`{"\ue000":0,"\ud800\udc00":1,"a":{"z":0,"a":1}}`, "{\"a\":{\"a\":1,\"z\":0},\"𐀀\":1,\"\ue000\":0}"},
	} {
		input, err := ParsePayload([]byte(test.source))
		if err != nil || string(input.JSON()) != test.canonical {
			t.Fatalf("canonical(%s) = %s, %v; want %s", test.source, input.JSON(), err, test.canonical)
		}
	}
	for _, source := range [][]byte{[]byte(`{"a":1,"\u0061":2}`), []byte(`"\ud800"`), {'"', 0xff, '"'}, []byte(`{} []`)} {
		if _, err := ParsePayload(source); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("invalid JSON accepted: %q: %v", source, err)
		}
	}
}
