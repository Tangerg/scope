package jsonschema_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

type digestInput struct {
	Data [2]byte `json:"data"`
}
type digestAlias [2]byte
type octet uint8
type encodedOctet uint8

func (encodedOctet) MarshalJSON() ([]byte, error) { return []byte(`"octet"`), nil }
func (encodedOctet) JSONSchemaModel() any         { return "" }

func assertSchemaAcceptsWire[T any](t *testing.T, value T) {
	t.Helper()
	schema, err := jsonschema.For[T]()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jsonv2.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(wire); err != nil {
		t.Fatalf("schema rejected %T wire %s: %v", value, wire, err)
	}
}

func TestByteArraysPublishTheirJSONV2WireShape(t *testing.T) {
	assertSchemaAcceptsWire(t, digestInput{Data: [2]byte{1, 2}})
	assertSchemaAcceptsWire(t, struct {
		Data digestAlias `json:"data"`
	}{Data: digestAlias{1, 2}})
	assertSchemaAcceptsWire(t, struct {
		Data [0]byte `json:"data"`
	}{})
	assertSchemaAcceptsWire(t, struct {
		Data [][2]byte `json:"data"`
	}{Data: [][2]byte{{1, 2}}})
	assertSchemaAcceptsWire(t, struct {
		Data [2]octet `json:"data"`
	}{Data: [2]octet{1, 2}})
	assertSchemaAcceptsWire(t, struct {
		Data []octet `json:"data"`
	}{Data: []octet{1, 2}})
	assertSchemaAcceptsWire(t, struct {
		Data []encodedOctet `json:"data"`
	}{Data: []encodedOctet{1, 2}})
}

func TestByteArrayToolAdmitsInputAndOutput(t *testing.T) {
	fn, err := tool.NewFunc(tool.FuncConfig{Name: "digest", Effect: tool.EffectSafe}, func(_ context.Context, in digestInput) (string, error) { return "done", nil })
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(fn)
	if err != nil {
		t.Fatal(err)
	}
	_, base64Err := binding.Contract().Prepare(tool.Request{Name: "digest", Arguments: `{"data":"AQI="}`}, tool.Admission{})
	_, arrayErr := binding.Contract().Prepare(tool.Request{Name: "digest", Arguments: `{"data":[1,2]}`}, tool.Admission{})
	if base64Err != nil || arrayErr == nil {
		t.Fatalf("expected base64 admission and numeric-array rejection, base64=%v array=%v", base64Err, arrayErr)
	}
	outFn, err := tool.NewFunc(tool.FuncConfig{Name: "make_digest", Effect: tool.EffectSafe}, func(_ context.Context, _ struct{}) (digestInput, error) { return digestInput{Data: [2]byte{1, 2}}, nil })
	if err != nil {
		t.Fatal(err)
	}
	outBinding, err := tool.Bind(outFn)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := outBinding.Contract().Prepare(tool.Request{Name: "make_digest", Arguments: `{}`}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = outBinding.Call(context.Background(), invocation)
	if err != nil {
		t.Fatalf("valid byte-array output rejected: %v", err)
	}
}

func TestByteSchemasEnforceEncodingAndArrayLength(t *testing.T) {
	for _, test := range []struct {
		name    string
		derive  func() (jsonschema.Schema, error)
		valid   []string
		invalid []string
	}{
		{"empty array", jsonschema.For[[0]byte], []string{`""`}, []string{`"AA=="`, `"!"`}},
		{"one byte", jsonschema.For[[1]byte], []string{`"AA=="`, `"/z=="`}, []string{`""`, `"AAA="`, `"AAAA"`, `"AA"`}},
		{"two bytes", jsonschema.For[[2]byte], []string{`"AQI="`, `"///="`}, []string{`"AA=="`, `"AAAA"`, `"xxxx!"`, `"AQI"`}},
		{"three bytes", jsonschema.For[[3]byte], []string{`"AAAA"`}, []string{`"AAA="`, `"AAAAAA=="`}},
		{"four bytes", jsonschema.For[[4]byte], []string{`"AAAAAA=="`}, []string{`"AAAAAAA="`, `"AAAA"`}},
		{"slice", jsonschema.For[[]byte], []string{`null`, `""`, `"AA=="`, `"AAA="`, `"AAAA"`}, []string{`"!"`, `"AA"`, `"A==="`, `"AA==AAAA"`, `"AA==\n"`, `"AA\r=="`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema, err := test.derive()
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range test.valid {
				if err = schema.Validate([]byte(raw)); err != nil {
					t.Errorf("valid %s: %v", raw, err)
				}
			}
			for _, raw := range test.invalid {
				if err = schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
					t.Errorf("invalid %s: got %v, want ErrValidation", raw, err)
				}
			}
		})
	}
}

func TestByteArrayBoundsSurviveWiderFieldTags(t *testing.T) {
	type input struct {
		Data [2]byte `json:"data" jsonschema:"minLength=0,maxLength=100,pattern=.*"`
	}
	schema, err := jsonschema.For[input]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"data":"AA=="}`, `{"data":"AAAA"}`, `{"data":"!!!!"}`} {
		if err := schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("field tags widened byte encoding for %s: %v", raw, err)
		}
	}
	assertSchemaAcceptsWire(t, input{Data: [2]byte{1, 2}})
	assertSchemaAcceptsWire(t, struct{ Data [4096]byte }{})
}

type shortBytes []byte

func (shortBytes) JSONSchemaConstraints() jsonschema.Constraints {
	return jsonschema.Constraints{MaxLength: new(uint64(4))}
}

func TestByteEncodingRetainsOwnedConstraints(t *testing.T) {
	schema, err := jsonschema.For[shortBytes]()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`null`, `""`, `"AQI="`} {
		if err = schema.Validate([]byte(raw)); err != nil {
			t.Errorf("valid %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`"AAAAAA=="`, `"!!!!"`} {
		if err = schema.Validate([]byte(raw)); !errors.Is(err, jsonschema.ErrValidation) {
			t.Errorf("invalid %s: %v", raw, err)
		}
	}
}
