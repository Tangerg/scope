package agent

import (
	"bytes"
	"errors"
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
		AnswerIsOutput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := spec.wire().value()
	if err != nil {
		t.Fatal(err)
	}
	if restored.key != spec.key || restored.answerIsOutput != spec.answerIsOutput || !restored.answerSchema.Equal(spec.answerSchema) {
		t.Fatalf("restored acceptance contract = %#v, want %#v", restored, spec)
	}
	// The restored contract enforces the same rule the wait opened with.
	invalid := controlValue(NewSignal(controlValue(ParseSignalID("signal:invalid")), WaitID{}, []byte(`123`)))
	if err := restored.admits(invalid); !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("restored contract admitted a schema-invalid answer: %v", err)
	}
}
