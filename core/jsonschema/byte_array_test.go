package jsonschema_test

import (
	"context"
	jsonv2 "encoding/json/v2"
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

func assertByteArrayWire[T any](t *testing.T, value T) {
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
	assertByteArrayWire(t, digestInput{Data: [2]byte{1, 2}})
	assertByteArrayWire(t, struct {
		Data digestAlias `json:"data"`
	}{Data: digestAlias{1, 2}})
	assertByteArrayWire(t, struct {
		Data [0]byte `json:"data"`
	}{})
	assertByteArrayWire(t, struct {
		Data [][2]byte `json:"data"`
	}{Data: [][2]byte{{1, 2}}})
	assertByteArrayWire(t, struct {
		Data [2]octet `json:"data"`
	}{Data: [2]octet{1, 2}})
	assertByteArrayWire(t, struct {
		Data []octet `json:"data"`
	}{Data: []octet{1, 2}})
	assertByteArrayWire(t, struct {
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
