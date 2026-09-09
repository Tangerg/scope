package jsonschema_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

type wirePerson struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func personSchema(t *testing.T) jsonschema.Schema {
	t.Helper()
	schema, err := jsonschema.For[wirePerson]()
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// TestSchemaJSONRoundTrip proves a compiled Schema survives the wire: it is the
// form a tool definition travels in, so a decoded Schema must still validate.
func TestSchemaJSONRoundTrip(t *testing.T) {
	schema := personSchema(t)

	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}

	var decoded jsonschema.Schema
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Valid() {
		t.Fatal("decoded Schema is not valid")
	}
	if err := decoded.Validate([]byte(`{"name":"ada","age":36}`)); err != nil {
		t.Fatalf("decoded Schema rejected a conforming value: %v", err)
	}
	if err := decoded.Validate([]byte(`{"name":"ada"}`)); err == nil {
		t.Fatal("decoded Schema accepted a value missing a required property")
	}
}

func TestZeroSchemaIsUnusable(t *testing.T) {
	var zero jsonschema.Schema
	if zero.Valid() {
		t.Fatal("zero Schema reports itself valid")
	}
	if zero.JSON() != nil {
		t.Fatalf("zero Schema returned a document: %s", zero.JSON())
	}
	if err := zero.Validate([]byte(`{}`)); !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("Validate error = %v, want ErrInvalid", err)
	}
	if _, err := zero.MarshalJSON(); !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("MarshalJSON error = %v, want ErrInvalid", err)
	}
}

func TestUnmarshalJSONRejectsNilReceiverAndInvalidDocuments(t *testing.T) {
	if err := (*jsonschema.Schema)(nil).UnmarshalJSON([]byte(`{}`)); !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("nil receiver error = %v, want ErrInvalid", err)
	}

	kept := personSchema(t)
	if err := kept.UnmarshalJSON([]byte(`{"type":"not-a-type"}`)); !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("UnmarshalJSON error = %v, want ErrInvalid", err)
	}
	if err := kept.Validate([]byte(`{"name":"ada","age":36}`)); err != nil {
		t.Fatalf("failed decode replaced the receiver: %v", err)
	}
}

// TestJSONReturnsAnIndependentDocument keeps the compiled schema immutable:
// handing out the backing slice would let a caller corrupt every later
// validation.
func TestJSONReturnsAnIndependentDocument(t *testing.T) {
	schema := personSchema(t)
	document := schema.JSON()
	if len(document) == 0 {
		t.Fatal("JSON returned an empty document")
	}
	document[0] = 'X'
	if schema.JSON()[0] == 'X' {
		t.Fatal("JSON aliases the compiled document")
	}
}

// TestNormalizeRejectsUnusableDocuments pins every rejection reason of the
// document gate, because each one describes a payload a provider could hand us
// and none of them may reach the compiler.
func TestNormalizeRejectsUnusableDocuments(t *testing.T) {
	cases := map[string][]byte{
		"empty":              nil,
		"invalid JSON":       []byte(`{`),
		"duplicate keys":     []byte(`{"type":"object","type":"array"}`),
		"invalid UTF-8":      []byte("{\"title\":\"\xff\"}"),
		"multiple documents": []byte(`{} {}`),
		"trailing garbage":   []byte(`{} x`),
		"oversized":          oversizedDocument(),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := jsonschema.Parse(raw); !errors.Is(err, jsonschema.ErrInvalid) {
				t.Fatalf("Parse error = %v, want ErrInvalid", err)
			}
		})
	}
}

func oversizedDocument() []byte {
	var builder strings.Builder
	builder.WriteString(`{"title":"`)
	builder.WriteString(strings.Repeat("a", 1<<20))
	builder.WriteString(`"}`)
	return []byte(builder.String())
}

// TestValidateRejectsUndecodableValues separates "the value is not JSON" from
// "the value does not conform", so callers can tell a transport failure from a
// contract failure.
func TestValidateRejectsUndecodableValues(t *testing.T) {
	schema := personSchema(t)
	if err := schema.Validate([]byte(`{`)); err == nil {
		t.Fatal("Validate accepted malformed JSON")
	}
	if err := schema.Validate([]byte(`{"name":"ada","age":"36"}`)); err == nil {
		t.Fatal("Validate accepted a value with the wrong property type")
	}
}

type brokenModeler struct{}

type pointerModeler struct{}

func (*pointerModeler) JSONSchemaAlias() any { return "" }

func TestForRejectsPointerOnlyWireModelMethods(t *testing.T) {
	if _, err := jsonschema.For[pointerModeler](); !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("pointer-only schema model must not silently become an empty object: %v", err)
	}
}

func (brokenModeler) JSONSchemaAlias() any { return nil }

type brokenModelerHolder struct {
	Value brokenModeler `json:"value"`
}

// TestForRejectsAModelerReturningNil proves the derivation reports an error
// rather than panicking out of the reflector when an implementation breaks the
// Modeler contract.
func TestForRejectsAModelerReturningNil(t *testing.T) {
	if _, err := jsonschema.For[brokenModelerHolder](); !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("For error = %v, want ErrInvalid", err)
	}
}

type underivableModeler struct{}

func (underivableModeler) JSONSchemaAlias() any { return make(chan int) }

type underivableModelerHolder struct {
	Value underivableModeler `json:"value"`
}

// TestForRejectsAModelerWithAnUnderivableModel keeps a broken Modeler from
// escaping as a panic through the reflector: the failure has to arrive as an
// ordinary ErrInvalid at the derivation boundary.
func TestForRejectsAModelerWithAnUnderivableModel(t *testing.T) {
	if _, err := jsonschema.For[underivableModelerHolder](); !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("For error = %v, want ErrInvalid", err)
	}
}

// TestForRejectsTypesWithNoJSONRepresentation keeps unsupported Go types out of
// tool schemas instead of publishing a contract no provider can satisfy.
func TestForRejectsTypesWithNoJSONRepresentation(t *testing.T) {
	if _, err := jsonschema.For[chan int](); !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("For[chan int] error = %v, want ErrInvalid", err)
	}
	if _, err := jsonschema.For[func()](); !errors.Is(err, jsonschema.ErrInvalid) {
		t.Fatalf("For[func()] error = %v, want ErrInvalid", err)
	}
}

// TestForEncodesByteSlicesAsNullableBase64 documents the one wire shape the
// derivation overrides by hand, because encoding/json writes []byte as a
// base64 string and a naive reflection would publish an array of integers.
func TestForEncodesByteSlicesAsNullableBase64(t *testing.T) {
	type payload struct {
		Data []byte `json:"data"`
	}
	schema, err := jsonschema.For[payload]()
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate([]byte(`{"data":"aGVsbG8="}`)); err != nil {
		t.Fatalf("Validate rejected a base64 payload: %v", err)
	}
	if err := schema.Validate([]byte(`{"data":null}`)); err != nil {
		t.Fatalf("Validate rejected a null payload: %v", err)
	}
	if err := schema.Validate([]byte(`{"data":[104,105]}`)); err == nil {
		t.Fatal("Validate accepted a byte array instead of base64")
	}
}
