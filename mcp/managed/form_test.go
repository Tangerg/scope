package managed

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestFormPreservesAdmittedSchemaWithoutDiscardingConstraints(t *testing.T) {
	for _, raw := range []string{
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","title":"Decision","description":"Choose a destination","properties":{"city":{"type":"string","title":"City","description":"Destination","enum":["Paris","Berlin"],"default":"Paris","minLength":1.0,"maxLength":1e2},"confirmed":{"type":"boolean","default":false}},"required":["city"],"additionalProperties":false}`,
		`{"type":"object","properties":{"value":{"type":"string","default":"","minLength":0}}}`,
		`{"type":"object","properties":{"value":{"type":"string","enum":["","yes"]}}}`,
		`{"type":"object","properties":{"value":{"type":"boolean","default":true}},"required":[]}`,
		`{"type":"object","properties":{"value":{"type":"string","format":"date"}}}`,
		`{"type":"object","properties":{"value":{"type":"string","format":"date-time"}}}`,
		`{"type":"object","properties":{"value":{"type":"string","format":"email"}}}`,
		`{"type":"object","properties":{"value":{"type":"string","format":"uri"}}}`,
	} {
		wait := testFormWait(t, raw)
		projected, err := form(wait)
		if err != nil {
			t.Fatalf("supported form was narrowed: %s: %v", raw, err)
		}
		preserved, ok := projected.RequestedSchema.(json.RawMessage)
		if !ok || !bytes.Equal(preserved, wait.AnswerSchema.JSON()) {
			t.Fatalf("form projection changed the admitted schema: %s", raw)
		}
	}
}

func TestFormRejectsUnrepresentableConstraintsAtTheirOwner(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":true}`,
		`{"type":"object","properties":{"value":{"type":"string"}},"additionalProperties":{}}`,
		`{"type":"object","properties":{"value":{"type":"string"}},"x-domain-rule":"opaque"}`,
		`{"type":"object","properties":{"value":{"type":"string","x-domain-rule":"opaque"}}}`,
		`{"type":"object","properties":{"value":{"type":"string","contentEncoding":"base64"}}}`,
		`{"type":"object","properties":{"value":{"type":"string","minimum":0}}}`,
		`{"type":"object","properties":{"value":{"type":"string","format":""}}}`,
		`{"type":"object","properties":{"value":{"type":"string","default":null}}}`,
		`{"type":"object","properties":{"value":{"type":"string","default":[]}}}`,
		`{"type":"object","properties":{"value":{"type":"string","enum":[null]}}}`,
		`{"type":"object","properties":{"value":{"type":"string","enum":["yes",null]}}}`,
		`{"type":"object","properties":{"value":{"type":"boolean","default":null}}}`,
		`{"type":"object","properties":{"value":{"type":"boolean","minLength":0}}}`,
		`{"type":"object","properties":{"value":{"type":"boolean","maxLength":10}}}`,
		`{"type":"object","properties":{"value":{"type":"boolean","enum":["yes"]}}}`,
		`{"type":"object","properties":{"value":{"type":"boolean","format":"email"}}}`,
		`{"type":"object","properties":{}}`,
	} {
		if projected, err := form(testFormWait(t, raw)); projected != nil || !errors.Is(err, errForm) {
			t.Fatalf("unrepresentable constraint was discarded: %s: %v, %v", raw, projected, err)
		}
	}
}

func TestFormDoesNotReinterpretUnsupportedQuestions(t *testing.T) {
	for _, raw := range []string{`{"type":"object","properties":{"n":{"type":"integer"}}}`, `{"type":"object","properties":{"n":{"type":"number"}}}`, `{"type":"array"}`, `{"type":"object","properties":{"a":{"type":"object"}}}`, `{"type":"object","properties":{"a":{"type":"string","pattern":"secret"}}}`, `{"type":"object","$defs":{"X":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/X"}}}`, `{"type":"object","anyOf":[{}],"properties":{"a":{"type":"string"}}}`} {
		p, err := form(testFormWait(t, raw))
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
	wait, err := run.ParseWaitKey("wait:test")
	if err != nil {
		t.Fatal(err)
	}
	destination := target{Owner: root, Wait: wait}
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
		v, err := form(testFormWait(t, string(schema)))
		if (err == nil) != tc.ok || tc.ok && v.Message != "Choose" {
			t.Fatalf("%s => %v, %v", tc.field, v, err)
		}
	}
}

func testFormWait(t *testing.T, schema string) run.Wait {
	t.Helper()
	parsed, err := jsonschema.Parse([]byte(schema))
	if err != nil {
		t.Fatal(err)
	}
	question, err := run.EncodePayload("Choose")
	if err != nil {
		t.Fatal(err)
	}
	return run.Wait{Question: question, AnswerSchema: parsed}
}
