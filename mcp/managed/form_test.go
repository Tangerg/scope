package managed

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
)

func TestFormDoesNotReinterpretUnsupportedQuestions(t *testing.T) {
	for _, raw := range []string{`{"type":"object","properties":{"n":{"type":"integer"}}}`, `{"type":"object","properties":{"n":{"type":"number"}}}`, `{"type":"array"}`, `{"type":"object","properties":{"a":{"type":"object"}}}`, `{"type":"object","properties":{"a":{"type":"string","pattern":"secret"}}}`, `{"type":"object","properties":{"a":{"$ref":"#/$defs/X"}}}`, `{"type":"object","anyOf":[],"properties":{"a":{"type":"string"}}}`} {
		p, err := form(run.PendingWait{Question: json.RawMessage(`"Your answer?"`), AnswerSchema: json.RawMessage(raw)})
		if p != nil || !errors.Is(err, errForm) {
			t.Fatalf("%s => %v %v", raw, p, err)
		}
	}
}
func TestExpiryAndOpaqueTokenAuthentication(t *testing.T) {
	key := [32]byte{7}
	signer, err := newIssuer(key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	root, err := run.ParseProcessID("root")
	if err != nil {
		t.Fatal(err)
	}
	wait, err := run.ParseWaitID("wait:test")
	if err != nil {
		t.Fatal(err)
	}
	destination := target{Root: root, Owner: root, Wait: wait}
	auth := []byte("host/session/arguments")
	token, err := signer.seal(destination, auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = signer.open(token, []byte("other")); !errors.Is(err, errContinuation) {
		t.Fatal(err)
	}
	signer.lifetime = -time.Nanosecond
	expired, err := signer.seal(destination, auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = signer.open(expired, auth); !errors.Is(err, errContinuation) {
		t.Fatal(err)
	}
}

func TestNativeFormVariants(t *testing.T) {
	for _, tc := range []struct {
		field string
		ok    bool
	}{
		{`{"type":"boolean","default":false}`, true}, {`{"type":"string","enum":["Paris"],"default":"Paris","minLength":1}`, true}, {`{"type":"string","format":"email"}`, true},
		{`{"type":"boolean","default":"yes"}`, false}, {`{"type":"string","enum":[7]}`, false}, {`{"type":"string","format":"unrenderable"}`, false},
	} {
		schema := json.RawMessage(`{"type":"object","properties":{"answer":` + tc.field + `}}`)
		v, err := form(run.PendingWait{Question: json.RawMessage(`"Choose"`), AnswerSchema: schema})
		if (err == nil) != tc.ok || tc.ok && v.Message != "Choose" {
			t.Fatalf("%s => %v, %v", tc.field, v, err)
		}
	}
}
