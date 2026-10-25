package jsonschema_test

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/speech"

	"github.com/Tangerg/scope/core/jsonschema"
)

type enumFixture string

func (enumFixture) JSONSchemaEnum() []string { return []string{"alpha", "beta"} }

type patternFixture string

func (patternFixture) JSONSchemaPattern() string { return "^[a-z]+$" }

func TestForDerivesStringConstraintsFromTheType(t *testing.T) {
	type payload struct {
		Mode enumFixture    `json:"mode"`
		Name patternFixture `json:"name"`
	}
	schema, err := jsonschema.For[payload]()
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range []string{`{"mode":"gamma","name":"ok"}`, `{"mode":"alpha","name":"NO"}`} {
		if err := schema.Validate([]byte(document)); err == nil {
			t.Errorf("%s bypassed a type-derived constraint", document)
		}
	}
	if err := schema.Validate([]byte(`{"mode":"alpha","name":"ok"}`)); err != nil {
		t.Fatalf("conforming value rejected: %v", err)
	}
}

type enumAndPatternFixture string

func (enumAndPatternFixture) JSONSchemaEnum() []string  { return []string{"alpha", "BAD"} }
func (enumAndPatternFixture) JSONSchemaPattern() string { return "^[a-z]+$" }

func TestForKeepsFieldTagsFromWideningTypeOwnedStringConstraints(t *testing.T) {
	type widened struct {
		Mode enumFixture    `json:"mode" jsonschema:"enum=gamma"`
		Name patternFixture `json:"name" jsonschema:"pattern=.*"`
	}
	widenedSchema, err := jsonschema.For[widened]()
	if err != nil {
		t.Fatal(err)
	}
	// A field tag cannot admit a value the owning type forbids: "gamma" is outside
	// the type enum and "NO" violates the type pattern regardless of the tags.
	for _, document := range []string{`{"mode":"gamma","name":"ok"}`, `{"mode":"alpha","name":"NO"}`} {
		if validationErr := widenedSchema.Validate([]byte(document)); validationErr == nil {
			t.Errorf("%s widened a type-owned constraint through a field tag", document)
		}
	}

	type composed struct {
		Value enumAndPatternFixture `json:"value"`
	}
	combined, err := jsonschema.For[composed]()
	if err != nil {
		t.Fatal(err)
	}
	if validationErr := combined.Validate([]byte(`{"value":"alpha"}`)); validationErr != nil {
		t.Fatalf("value satisfying both enum and pattern rejected: %v", validationErr)
	}
	// A type owning both an enum and a pattern applies both; neither is dropped.
	// "BAD" is enumerated but violates the pattern, so the pattern must still reject it.
	if validationErr := combined.Validate([]byte(`{"value":"BAD"}`)); validationErr == nil {
		t.Fatal("type-owned pattern was dropped when the type also owns an enum")
	}
	if validationErr := combined.Validate([]byte(`{"value":"gamma"}`)); validationErr == nil {
		t.Fatal("type-owned enum was dropped when the type also owns a pattern")
	}
}

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

func (richFixture) JSONSchemaModel() any { return richFixtureModel{} }

type nestedRichFixture struct{}

type nestedRichFixtureModel struct {
	Value richFixture `json:"value"`
	Wire  wireFixture `json:"wire"`
}

func (nestedRichFixture) JSONSchemaModel() any { return nestedRichFixtureModel{} }

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
	Next  *recursiveRichFixture `json:"next,omitzero"`
}

func (recursiveRichFixture) JSONSchemaModel() any { return recursiveRichFixtureModel{} }

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
	if decodeErr := jsonv2.Unmarshal(schema.JSON(), &document); decodeErr != nil {
		t.Fatal(decodeErr)
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
	sample := response{
		Chat:   chat.Output{Message: new(chat.NewAssistantMessage(chat.NewTextPart("answer"))), FinishReason: chat.FinishReasonStop},
		Speech: speech.Output{Audio: []byte("audio")},
	}
	encoded, err := jsonv2.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	if validationErr := schema.Validate(encoded); validationErr != nil {
		t.Fatalf("derived schema rejected real wire %s: %v", encoded, validationErr)
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
		Score     float64   `json:"score,omitzero" jsonschema:"minimum=-1.5,maximum=2.5"`
	}

	contract, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := jsonv2.Unmarshal(contract.JSON(), &schema); err != nil {
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
		Limit  *int              `json:"limit,omitzero"`
		Value  any               `json:"value,omitzero"`
		Debug  bool              `json:"debug,omitzero"`
	}
	type recursive struct {
		Next *recursive `json:"next,omitzero"`
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
	if decodeErr := jsonv2.Unmarshal(collection.JSON(), &collectionSchema); decodeErr != nil {
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
	if err := encoded.Validate([]byte(`{"value":"42"}`)); err != nil {
		t.Fatalf("string-encoded field rejected its wire representation: %v", err)
	}
	if err := encoded.Validate([]byte(`{"value":42}`)); err == nil {
		t.Fatal("string-encoded field accepted a JSON number")
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

func TestForPreservesIntegerRepresentationBounds(t *testing.T) {
	type input struct {
		Signed   int8     `json:"signed,omitzero"`
		Unsigned uint8    `json:"unsigned,omitzero"`
		Wide     int64    `json:"wide,omitzero"`
		Huge     uint64   `json:"huge,omitzero"`
		Tagged   uint8    `json:"tagged,omitzero" jsonschema:"minimum=-10,maximum=300"`
		Nested   []uint16 `json:"nested,omitempty"`
	}
	schema, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		raw   string
		valid bool
	}{
		{`{"signed":-128}`, true}, {`{"signed":127}`, true},
		{`{"signed":-129}`, false}, {`{"signed":128}`, false},
		{`{"unsigned":0}`, true}, {`{"unsigned":255}`, true},
		{`{"unsigned":-1}`, false}, {`{"unsigned":256}`, false},
		{`{"wide":-9223372036854775808}`, true}, {`{"wide":9223372036854775807}`, true},
		{`{"wide":-9223372036854775809}`, false}, {`{"wide":9223372036854775808}`, false},
		{`{"huge":18446744073709551615}`, true}, {`{"huge":18446744073709551616}`, false},
		{`{"tagged":255}`, true}, {`{"tagged":256}`, false}, {`{"tagged":-1}`, false},
		{`{"nested":[65535]}`, true}, {`{"nested":[65536]}`, false},
	} {
		if err := schema.Validate([]byte(test.raw)); (err == nil) != test.valid {
			t.Errorf("Validate(%s) = %v, want valid=%t", test.raw, err, test.valid)
		}
	}
}

func TestParseRejectsImplicitExternalResources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "external.json")
	ref, err := jsonv2.Marshal(map[string]string{"$ref": "file://" + path})
	if err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{`{"type":"string"}`, `{"type":"number"}`} {
		if writeErr := os.WriteFile(path, []byte(contents), 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, parseErr := jsonschema.Parse(ref); !errors.Is(parseErr, jsonschema.ErrInvalid) {
			t.Fatalf("external reference = %v", parseErr)
		}
	}
	if removeErr := os.Remove(path); removeErr != nil {
		t.Fatal(removeErr)
	}
	if _, parseErr := jsonschema.Parse(ref); !errors.Is(parseErr, jsonschema.ErrInvalid) {
		t.Fatalf("missing external reference = %v", parseErr)
	}
	schema, err := jsonschema.Parse([]byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`"valid"`)); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`1`)); err == nil {
		t.Fatal("internal reference was ignored")
	}
}

type constrainedScore float64

func (constrainedScore) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Minimum: new(float64(0)), Maximum: new(float64(1))}
}

type constrainedRequest struct {
	Limit int              `json:"limit"`
	Names []string         `json:"names"`
	Score constrainedScore `json:"score" jsonschema:"minimum=-100,maximum=100"`
}

func (constrainedRequest) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{
		"limit": {Minimum: new(float64(1)), Maximum: new(float64(20))},
		"names": {MinItems: new(uint64(1)), MaxItems: new(uint64(2))},
	}}
}

func TestForProjectsDomainConstraintsWithoutReplacingWireTypes(t *testing.T) {
	schema, err := jsonschema.For[constrainedRequest]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"limit":0,"names":["one"],"score":0.5}`,
		`{"limit":21,"names":["one"],"score":0.5}`,
		`{"limit":1,"names":[],"score":0.5}`,
		`{"limit":1,"names":["a","b","c"],"score":0.5}`,
		`{"limit":1,"names":[1],"score":0.5}`,
		`{"limit":1,"names":["a"],"score":1.1}`,
	} {
		if err := schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("accepted %s: %v", raw, err)
		}
	}
	if err := schema.Validate([]byte(`{"limit":20,"names":["a","b"],"score":1}`)); err != nil {
		t.Fatal(err)
	}
}

type constrainedLabel string

func (constrainedLabel) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{MinLength: new(uint64(1)), MaxLength: new(uint64(3))}
}

type constrainedSmallInt int8

func (constrainedSmallInt) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Minimum: new(float64(-4)), Maximum: new(float64(4))}
}

func TestForPreservesOwnedScalarBoundsAcrossFieldTags(t *testing.T) {
	schema, err := jsonschema.For[struct {
		Name constrainedLabel    `json:"name" jsonschema:"minLength=0,maxLength=100"`
		Size constrainedSmallInt `json:"size" jsonschema:"minimum=-100,maximum=100"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"name":"","size":0}`, `{"name":"long","size":0}`, `{"name":"ok","size":5}`, `{"name":"ok","size":-5}`} {
		if err := schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("accepted %s: %v", raw, err)
		}
	}
	if err := schema.Validate([]byte(`{"name":"ok","size":4}`)); err != nil {
		t.Fatal(err)
	}
}

type missingConstraintProperty struct{ Value string }

func (missingConstraintProperty) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{"missing": {MinLength: new(uint64(1))}}}
}

type invalidScalarConstraint int

func (invalidScalarConstraint) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Properties: map[string]jsonschema.Constraints{"missing": {MinLength: new(uint64(1))}}}
}

type unsupportedConstraint bool

func (unsupportedConstraint) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{Minimum: new(float64(0))}
}

type pointerConstraint string

func (*pointerConstraint) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{MinLength: new(uint64(1))}
}

func TestForRejectsInvalidConstraintDeclarations(t *testing.T) {
	for _, derive := range []func() (jsonschema.Schema, error){
		jsonschema.For[missingConstraintProperty],
		jsonschema.For[invalidScalarConstraint],
		jsonschema.For[unsupportedConstraint],
		jsonschema.For[pointerConstraint],
	} {
		if _, err := derive(); !errors.Is(err, jsonschema.ErrInvalid) {
			t.Fatalf("invalid constraint declaration: %v", err)
		}
	}
}

func TestSchemaEqualUsesValidCanonicalContracts(t *testing.T) {
	first, err := jsonschema.Parse([]byte(`{"type":"string","maxLength":3}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := jsonschema.Parse([]byte(`{ "maxLength": 3, "type": "string" }`))
	if err != nil {
		t.Fatal(err)
	}
	different, err := jsonschema.For[int]()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(second) || first.Equal(different) || first.Equal(jsonschema.Schema{}) || (jsonschema.Schema{}).Equal(jsonschema.Schema{}) {
		t.Fatal("schema equality lost canonical validity or contract identity")
	}
}
