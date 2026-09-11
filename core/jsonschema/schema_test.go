package jsonschema_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/speech"
)

func TestValidateRequiresStrictJSONAndPreservesNumbers(t *testing.T) {
	schema, err := jsonschema.Parse([]byte(`true`))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"value":"first","value":"second"}`,
		`{"nested":{"value":1,"value":2}}`,
		"{\"value\":\"\xff\"}",
		`{"value":"\ud800"}`,
		`{} {}`,
		``,
	} {
		if validationErr := schema.Validate([]byte(raw)); validationErr == nil {
			t.Errorf("Validate(%q) accepted invalid RFC 7493 JSON", raw)
		}
	}
	schema, err = jsonschema.Parse([]byte(`{"const":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`9007199254740993`)); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`9007199254740992`)); err == nil {
		t.Fatal("distinct integers above float64 precision compared equal")
	}
}

type wireFixture struct {
	Metadata  map[string]json.RawMessage `json:"metadata"`
	Signature []byte                     `json:"signature"`
}

type richFixture struct{}

type richFixtureModel struct {
	Value int `json:"value" jsonschema:"minimum=1"`
}

func (richFixture) JSONSchemaAlias() any { return richFixtureModel{} }

type nestedRichFixture struct{}

type nestedRichFixtureModel struct {
	Value richFixture `json:"value"`
	Wire  wireFixture `json:"wire"`
}

func (nestedRichFixture) JSONSchemaAlias() any { return nestedRichFixtureModel{} }

func TestForRetainsDefinitionsInsideNestedWireModels(t *testing.T) {
	type result struct {
		Nested nestedRichFixture `json:"nested"`
	}
	schema, err := jsonschema.For[result]()
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`{"nested":{"value":{"value":1},"wire":{"metadata":{},"signature":null}}}`)); err != nil {
		t.Fatalf("nested wire model rejected its public representation: %v", err)
	}
	if err := schema.Validate([]byte(`{"nested":{"value":{"value":0},"wire":{"metadata":{},"signature":null}}}`)); err == nil {
		t.Fatal("nested wire model lost its value constraint")
	}
}

type recursiveRichFixture struct{}

type recursiveRichFixtureModel struct {
	Value int                   `json:"value" jsonschema:"minimum=1"`
	Next  *recursiveRichFixture `json:"next,omitempty"`
}

func (recursiveRichFixture) JSONSchemaAlias() any { return recursiveRichFixtureModel{} }

func TestForRetainsRecursiveWireModelReferences(t *testing.T) {
	schema, err := jsonschema.For[recursiveRichFixture]()
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`{"value":1,"next":{"value":2,"next":{"value":3}}}`)); err != nil {
		t.Fatalf("recursive wire model rejected valid content: %v", err)
	}
	if err := schema.Validate([]byte(`{"value":1,"next":{"value":2,"next":{"value":0}}}`)); err == nil {
		t.Fatal("recursive wire model lost its nested constraint")
	}
}

func TestForMatchesEncodingJSONAndOwnsDocument(t *testing.T) {
	schema, err := jsonschema.For[wireFixture]()
	if err != nil {
		t.Fatal(err)
	}
	valid := []byte(`{"metadata":{"object":{"id":1},"boolean":true},"signature":"AAEC/w=="}`)
	if err := schema.Validate(valid); err != nil {
		t.Fatalf("Validate(valid) error = %v; schema = %s", err, schema.JSON())
	}
	if err := schema.Validate([]byte(`{"metadata":{},"signature":[1,2]}`)); err == nil {
		t.Fatal("Validate(array-encoded bytes) succeeded")
	}
	if err := schema.Validate([]byte(`{"metadata":{},"signature":null}`)); err != nil {
		t.Fatalf("Validate(nil byte slice) error = %v", err)
	}
	first := schema.JSON()
	first[0] = '['
	if second := schema.JSON(); second[0] != '{' {
		t.Fatal("JSON returned aliased bytes")
	}
	if !bytes.Contains(schema.JSON(), []byte(`"contentEncoding":"base64"`)) {
		t.Fatalf("byte slice schema = %s", schema.JSON())
	}
}

func TestForQualifiesSameNamedTypesFromDifferentPackages(t *testing.T) {
	type response struct {
		Chat   chat.Output   `json:"chat"`
		Speech speech.Output `json:"speech"`
	}
	schema, err := jsonschema.For[response]()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Properties map[string]struct {
			Ref string `json:"$ref"`
		} `json:"properties"`
		Definitions map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(schema.JSON(), &document); err != nil {
		t.Fatal(err)
	}
	chatRef := document.Properties["chat"].Ref
	speechRef := document.Properties["speech"].Ref
	if chatRef == "" || speechRef == "" || chatRef == speechRef {
		t.Fatalf("same-named types have ambiguous refs: chat=%q speech=%q", chatRef, speechRef)
	}
	for _, ref := range []string{chatRef, speechRef} {
		name := strings.TrimPrefix(ref, "#/$defs/")
		if _, exists := document.Definitions[name]; !exists {
			t.Fatalf("ref %q has no matching definition", ref)
		}
	}
}

func TestForUsesRichValueSchemaModel(t *testing.T) {
	schema, err := jsonschema.For[richFixture]()
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`{"value":1}`)); err != nil {
		t.Fatalf("Validate(valid modeled value) error = %v; schema = %s", err, schema.JSON())
	}
	if err := schema.Validate([]byte(`{"value":0}`)); err == nil {
		t.Fatal("Validate(value below modeled minimum) succeeded")
	}
}

func TestForMatchesJSONFieldAndSchemaTagSemantics(t *testing.T) {
	type option struct {
		Label string `json:"label"`
	}
	type input struct {
		Operation string    `json:"operation" jsonschema:"enum=list,enum=load,minLength=4,maxLength=4,pattern=^[a-z]+$" jsonschema_description:"Operation to run."`
		Options   []option  `json:"options,omitempty" jsonschema:"minItems=1,maxItems=4"`
		Ignored   string    `json:"-"`
		When      time.Time `json:"when,omitzero"`
		Score     float64   `json:"score,omitempty" jsonschema:"minimum=-1.5,maximum=2.5"`
	}

	contract, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(contract.JSON(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema = %s, want strict object", contract.JSON())
	}
	properties := schema["properties"].(map[string]any)
	if _, exists := properties["Ignored"]; exists {
		t.Fatalf("schema includes ignored field: %s", contract.JSON())
	}
	operation := properties["operation"].(map[string]any)
	if operation["description"] != "Operation to run." || len(operation["enum"].([]any)) != 2 ||
		operation["minLength"] != float64(4) || operation["maxLength"] != float64(4) || operation["pattern"] != "^[a-z]+$" {
		t.Fatalf("operation schema = %#v", operation)
	}
	required := schema["required"].([]any)
	if len(required) != 1 || required[0] != "operation" {
		t.Fatalf("required = %#v, want [operation]", required)
	}
	options := properties["options"].(map[string]any)
	if options["minItems"] != float64(1) || options["maxItems"] != float64(4) {
		t.Fatalf("options schema = %#v", options)
	}
	when := properties["when"].(map[string]any)
	if when["type"] != "string" || when["format"] != "date-time" {
		t.Fatalf("time schema = %#v", when)
	}
	score := properties["score"].(map[string]any)
	if score["minimum"] != -1.5 || score["maximum"] != 2.5 {
		t.Fatalf("score schema = %#v", score)
	}
}

func TestForSupportsCompositePointerAndEncodingSemantics(t *testing.T) {
	type input struct {
		Names  []string          `json:"names"`
		Labels map[string]string `json:"labels"`
		Limit  *int              `json:"limit,omitempty"`
		Value  any               `json:"value,omitempty"`
		Debug  bool              `json:"debug,omitzero"`
	}
	type recursive struct {
		Next *recursive `json:"next,omitempty"`
	}
	type badPattern struct {
		Value string `json:"value" jsonschema:"pattern=["`
	}
	type stringEncoded struct {
		Value int `json:"value,string"`
	}

	if _, err := jsonschema.For[*input](); err != nil {
		t.Fatal(err)
	}
	collection, err := jsonschema.For[map[string][]int]()
	if err != nil {
		t.Fatal(err)
	}
	var collectionSchema map[string]any
	if decodeErr := json.Unmarshal(collection.JSON(), &collectionSchema); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	additional := collectionSchema["additionalProperties"].(map[string]any)
	if additional["type"] != "array" {
		t.Fatalf("map schema = %s", collection.JSON())
	}
	recursiveSchema, err := jsonschema.For[recursive]()
	if err != nil {
		t.Fatalf("recursive schema: %v", err)
	}
	if !strings.Contains(string(recursiveSchema.JSON()), `"$ref"`) || !strings.Contains(string(recursiveSchema.JSON()), `"$defs"`) {
		t.Fatalf("recursive schema does not use references: %s", recursiveSchema.JSON())
	}
	if _, schemaErr := jsonschema.For[badPattern](); schemaErr == nil {
		t.Fatal("invalid pattern succeeded")
	}
	encoded, err := jsonschema.For[stringEncoded]()
	if err != nil {
		t.Fatalf("encoding/json string option: %v", err)
	}
	if !strings.Contains(string(encoded.JSON()), `"value":{"type":"string"}`) {
		t.Fatalf("string-encoded field schema = %s", encoded.JSON())
	}
	if _, err := jsonschema.For[chan int](); err == nil {
		t.Fatal("unsupported Go type succeeded")
	}
}

func TestParseRejectsMalformedAndUnresolvedSchemas(t *testing.T) {
	for _, raw := range [][]byte{
		nil,
		[]byte(`[]`),
		[]byte(`{"type":"not-a-type"}`),
		[]byte(`{"minLength":-1}`),
		[]byte(`{"$ref":"https://example.com/external-schema"}`),
	} {
		if _, err := jsonschema.Parse(raw); !errors.Is(err, jsonschema.ErrInvalid) {
			t.Fatalf("Parse(%q) error = %v, want ErrInvalid", raw, err)
		}
	}
}
