package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

type jsonWireFixture struct {
	Metadata  map[string]json.RawMessage `json:"metadata"`
	Signature []byte                     `json:"signature"`
}

func TestPayloadConformsToDerivedSchema(t *testing.T) {
	schema, err := jsonschema.For[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := EncodePayload(wireFixture{Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if validateInputErr := schema.Validate(valid.JSON()); validateInputErr != nil {
		t.Fatalf("Validate(valid) error = %v", validateInputErr)
	}
	invalid, err := ParsePayload([]byte(`{"message":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(invalid.JSON()); !errors.Is(err, jsonschema.ErrValidation) {
		t.Fatalf("Validate(invalid) error = %v, want jsonschema.ErrValidation; schema = %s", err, schema.JSON())
	}
}

func TestChildSpecConformsToDerivedSchema(t *testing.T) {
	key, err := ParseChildKey("candidate")
	if err != nil {
		t.Fatal(err)
	}
	input, err := EncodePayload(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	spec := childTestSpec(key, newChildTestDeployment(t).DeploymentRef(), input)
	schema, err := jsonschema.For[ChildSpec]()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodePayload(spec)
	if err != nil {
		t.Fatal(err)
	}
	if validationErr := schema.Validate(encoded.JSON()); validationErr != nil {
		t.Fatalf("a canonical child request cannot cross a typed strategy input: %v; schema=%s", validationErr, schema.JSON())
	}
}

func TestPayloadSchemaMatchesEncodingJSONWireTypes(t *testing.T) {
	schema, err := jsonschema.For[jsonWireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := EncodePayload(jsonWireFixture{
		Metadata: map[string]json.RawMessage{
			"array":   json.RawMessage(`[1,"two"]`),
			"boolean": json.RawMessage(`true`),
			"null":    json.RawMessage(`null`),
			"number":  json.RawMessage(`3`),
			"object":  json.RawMessage(`{"id":"chunk"}`),
			"string":  json.RawMessage(`"deepseek"`),
		},
		Signature: []byte{0, 1, 2, 255},
	})
	if err != nil {
		t.Fatal(err)
	}
	if validateOutputErr := schema.Validate(valid.JSON()); validateOutputErr != nil {
		t.Fatalf("Validate(valid JSON wire values) error = %v; schema = %s", validateOutputErr, schema.JSON())
	}

	nilSignature, err := EncodePayload(jsonWireFixture{Metadata: map[string]json.RawMessage{}})
	if err != nil {
		t.Fatal(err)
	}
	if validateOutputErr := schema.Validate(nilSignature.JSON()); validateOutputErr != nil {
		t.Fatalf("Validate(nil byte slice) error = %v; schema = %s", validateOutputErr, schema.JSON())
	}

	arraySignature, err := ParsePayload([]byte(`{"metadata":{"provider":"deepseek"},"signature":[1,2,3]}`))
	if err != nil {
		t.Fatal(err)
	}
	if validateOutputErr := schema.Validate(arraySignature.JSON()); !errors.Is(validateOutputErr, jsonschema.ErrValidation) {
		t.Fatalf("Validate(array signature) error = %v, want jsonschema.ErrValidation", validateOutputErr)
	}
	if !bytes.Contains(schema.JSON(), []byte(`"contentEncoding":"base64"`)) {
		t.Fatalf("derived schema does not identify the byte-slice encoding: %s", schema.JSON())
	}

	_, err = EncodePayload(jsonWireFixture{
		Metadata: map[string]json.RawMessage{"invalid": json.RawMessage(`{`)},
	})
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("EncodePayload(invalid RawMessage) error = %v, want ErrInvalidPayload", err)
	}
}
