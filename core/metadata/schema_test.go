package metadata_test

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/metadata"
)

func TestDerivedSchemaPreservesNullableMetadataWire(t *testing.T) {
	schema, err := jsonschema.For[metadata.Map]()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []metadata.Map{nil, {}, {"value": []byte(`9007199254740993`)}} {
		encoded, encodeErr := jsonv2.Marshal(value)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if validationErr := schema.Validate(encoded); validationErr != nil {
			t.Fatalf("wire=%s schema=%s error=%v", encoded, schema.JSON(), validationErr)
		}
	}
	for _, invalid := range []string{`true`, `1`, `[]`, `"object"`} {
		if validationErr := schema.Validate([]byte(invalid)); validationErr == nil {
			t.Fatalf("non-map value accepted: %s", invalid)
		}
	}
	extensionSchema, err := jsonschema.For[metadata.Extensions]()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(metadata.Extensions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := extensionSchema.Validate(encoded); err != nil {
		t.Fatalf("zero Extensions wire %s: %v", encoded, err)
	}
}
