package agent

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestExternalWaitSpecEnforcesAnswerSchema(t *testing.T) {
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := NewExternalWaitSpec(ExternalWaitSpecConfig{Key: controlValue(ParseWaitKey("gate")), AnswerSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	invalid := controlValue(NewSignal(controlValue(ParseSignalID("signal:invalid")), WaitID{}, []byte(`123`)))
	if err := spec.admits(invalid); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("schema-invalid answer admitted: %v", err)
	}
	valid := controlValue(NewSignal(controlValue(ParseSignalID("signal:valid")), WaitID{}, []byte(`"answer"`)))
	if err := spec.admits(valid); err != nil {
		t.Fatalf("schema-valid answer rejected: %v", err)
	}
}

func TestExternalWaitSpecRejectsUndeliverableAnswer(t *testing.T) {
	// A payload at the size limit is a valid Signal payload, but delivering it as
	// the wait's output wraps it in a Signal envelope that exceeds the same limit.
	// An AnswerIsOutput wait must reject it at admission, so it never admits an
	// answer it cannot deliver; without AnswerIsOutput the same payload is fine.
	payload := append(append([]byte{'"'}, bytes.Repeat([]byte("a"), MaxPayloadBytes-2)...), '"')
	answer := controlValue(NewSignal(controlValue(ParseSignalID("signal:oversized")), WaitID{}, payload))

	output, err := NewExternalWaitSpec(ExternalWaitSpecConfig{Key: controlValue(ParseWaitKey("gate")), AnswerIsOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if admitErr := output.admits(answer); !errors.Is(admitErr, ErrSignalRejected) {
		t.Fatalf("undeliverable answer admitted: %v", admitErr)
	}

	plain, err := NewExternalWaitSpec(ExternalWaitSpecConfig{Key: controlValue(ParseWaitKey("gate"))})
	if err != nil {
		t.Fatal(err)
	}
	if admitErr := plain.admits(answer); admitErr != nil {
		t.Fatalf("a wait that does not return the answer rejected a valid payload: %v", admitErr)
	}
}

func TestExternalWaitSpecWireRoundTripsTheAcceptanceContract(t *testing.T) {
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := NewExternalWaitSpec(ExternalWaitSpecConfig{
		Key:            controlValue(ParseWaitKey("gate")),
		AnswerSchema:   schema,
		MaxAnswerBytes: 8,
		AnswerIsOutput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := spec.wire().value()
	if err != nil {
		t.Fatal(err)
	}
	if restored.key != spec.key || restored.maxAnswerBytes != spec.maxAnswerBytes || restored.answerIsOutput != spec.answerIsOutput || !restored.answerSchema.Equal(spec.answerSchema) {
		t.Fatalf("restored acceptance contract = %#v, want %#v", restored, spec)
	}
	invalid := controlValue(NewSignal(controlValue(ParseSignalID("signal:invalid")), WaitID{}, []byte(`123`)))
	if err := restored.admits(invalid); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("restored contract admitted a schema-invalid answer: %v", err)
	}
	oversized := controlValue(NewSignal(controlValue(ParseSignalID("signal:oversized")), WaitID{}, []byte(`"too long"`)))
	if err := restored.admits(oversized); !errors.Is(err, ErrSignalRejected) || !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("restored contract admitted an oversized answer: %v", err)
	}
}

func TestExternalWaitSpecBoundsCanonicalAnswerBytes(t *testing.T) {
	spec := controlValue(NewExternalWaitSpec(ExternalWaitSpecConfig{Key: controlValue(ParseWaitKey("bounded")), MaxAnswerBytes: 8}))
	for _, test := range []struct {
		name, value string
		accepted    bool
	}{
		{name: "exact bound", value: `"123456"`, accepted: true},
		{name: "one byte over", value: `"1234567"`},
		{name: "escaped exact bound", value: `"<"`, accepted: true},
		{name: "escaping exceeds bound", value: `"<<"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			signal := controlValue(NewSignal(controlValue(ParseSignalID("signal:bounded")), WaitID{}, []byte(test.value)))
			err := spec.admits(signal)
			if test.accepted && err != nil || !test.accepted && (!errors.Is(err, ErrSignalRejected) || !errors.Is(err, ErrPayloadTooLarge)) {
				t.Fatalf("answer %s admission: %v", test.value, err)
			}
		})
	}
}

func TestExternalWaitSpecRejectsInvalidAndMissingByteBounds(t *testing.T) {
	key := controlValue(ParseWaitKey("bounded"))
	for _, maximum := range []int{-1, MaxPayloadBytes + 1} {
		if _, err := NewExternalWaitSpec(ExternalWaitSpecConfig{Key: key, MaxAnswerBytes: maximum}); !errors.Is(err, ErrInvalidExternalWait) {
			t.Fatalf("answer limit %d: %v", maximum, err)
		}
	}
	for _, maximum := range []int{-1, 0, MaxPayloadBytes + 1} {
		if _, err := (externalWaitSpecWire{Key: key, MaxAnswerBytes: maximum}).value(); !errors.Is(err, ErrInvalidExternalWait) {
			t.Fatalf("persisted answer limit %d: %v", maximum, err)
		}
	}
	spec := controlValue(NewExternalWaitSpec(ExternalWaitSpecConfig{Key: key}))
	if spec.maxAnswerBytes != MaxPayloadBytes || !spec.Valid() || (ExternalWaitSpec{key: key}).Valid() {
		t.Fatal("wait validity or default answer bound differs from the payload contract")
	}
	effect := controlValue(NewWaitEffect(spec))
	if !strings.Contains(string(effect.Payload()), `"max_answer_bytes":67108864`) {
		t.Fatalf("persisted wait omitted its answer bound: %s", effect.Payload())
	}
}
