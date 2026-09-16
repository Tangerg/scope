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

func TestInputOwnsNormalizedJSON(t *testing.T) {
	source := json.RawMessage(` { "message": "hello" } `)
	input, err := ParseInput(source)
	if err != nil {
		t.Fatal(err)
	}
	source[3] = 'x'
	if got := string(input.JSON()); got != `{"message":"hello"}` {
		t.Fatalf("Input.JSON() = %s", got)
	}
	copyOfJSON := input.JSON()
	copyOfJSON[0] = '['
	if got := string(input.JSON()); got != `{"message":"hello"}` {
		t.Fatalf("Input shared returned bytes: %s", got)
	}
}

func TestInputRejectsMalformedMultipleAndDuplicateValues(t *testing.T) {
	for _, data := range []json.RawMessage{nil, []byte(`{"message":`), []byte(`{} {}`), []byte(`{"message":"first","message":"second"}`)} {
		if _, err := ParseInput(data); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("ParseInput(%q) error = %v, want ErrInvalidInput", data, err)
		}
	}
}

func TestTypedInputRejectsUnknownFields(t *testing.T) {
	input, err := ParseInput([]byte(`{"message":"hello","unknown":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.Decode[wireFixture](); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("DecodeInput error = %v, want ErrInvalidInput", err)
	}
}

func TestOutputTypedRoundTrip(t *testing.T) {
	want := wireFixture{Message: "done"}
	output, err := EncodeOutput(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := output.Decode[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DecodeOutput() = %+v, want %+v", got, want)
	}
}

func TestWireZeroValuesAreInvalid(t *testing.T) {
	if (Input{}).Valid() || (Output{}).Valid() {
		t.Fatal("zero wire values reported valid")
	}
	if _, err := json.Marshal(Input{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("marshal Input error = %v, want ErrInvalidInput", err)
	}
	if _, err := json.Marshal(Output{}); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("marshal Output error = %v, want ErrInvalidOutput", err)
	}
}

func TestNormalizeJSONEnforcesNormalizedLimit(t *testing.T) {
	if _, err := normalizeJSON([]byte(`"<"`), 3); err == nil {
		t.Fatal("normalizeJSON accepted a value that exceeded the limit after normalization")
	}
}

func FuzzInputJSONRoundTrip(f *testing.F) {
	f.Add([]byte(`{"message":"hello"}`))
	f.Add([]byte(`[1,true,null]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		input, err := ParseInput(data)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Input
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
			if _, err := EncodeInput(value); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("input accepted invalid UTF-8: %v", err)
			}
			if _, err := EncodeOutput(value); !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("output accepted invalid UTF-8: %v", err)
			}
		})
	}
	want := wireFixture{Message: "中文 🌍 \ufffd"}
	input, err := EncodeInput(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := input.Decode[wireFixture]()
	if err != nil || got != want {
		t.Fatalf("Unicode changed: %+v, %v", got, err)
	}
	for _, number := range []json.Number{"9007199254740993", "1e400", "1.234567890123456789"} {
		input, err := EncodeInput(number)
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
