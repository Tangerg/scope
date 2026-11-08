package jsonschema_test

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func TestURLSchemaUsesJSONV2ObjectEncoding(t *testing.T) {
	value := url.URL{Scheme: "https", Host: "example.com", Path: "/file"}
	shape, err := jsonschema.For[url.URL]()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jsonv2.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = shape.Validate(wire); err != nil {
		t.Fatalf("actual URL wire %s rejected: %v", wire, err)
	}
	var decoded url.URL
	if err = jsonv2.Unmarshal(wire, &decoded); err != nil || !reflect.DeepEqual(decoded, value) {
		t.Fatalf("URL wire did not round trip: %#v, %v", decoded, err)
	}
}

func TestURLSchemaRejectsUnencodableUserinfo(t *testing.T) {
	shape, err := jsonschema.For[url.URL]()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jsonv2.Marshal(url.URL{})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{`{}`, `{"username":"alice"}`, `"alice"`} {
		invalid := bytes.Replace(wire, []byte(`"User":null`), []byte(`"User":`+member), 1)
		if bytes.Equal(invalid, wire) {
			t.Fatal("URL wire did not contain its null userinfo")
		}
		var decoded url.URL
		if err := jsonv2.Unmarshal(invalid, &decoded); err == nil {
			t.Fatalf("JSON v2 admitted unencodable userinfo: %s", invalid)
		}
		if err := shape.Validate(invalid); !errors.Is(err, jsonschema.ErrValidation) {
			t.Fatalf("schema admitted unencodable userinfo: %s, %v", invalid, err)
		}
	}
	for _, user := range []*url.Userinfo{url.User("alice"), url.UserPassword("alice", "secret")} {
		if _, err := jsonv2.Marshal(url.URL{User: user}); err == nil {
			t.Fatal("JSON v2 unexpectedly encoded non-nil userinfo")
		}
	}
}

func TestDurationSignatureRejectsMissingDefaultCodec(t *testing.T) {
	_, err := tool.NewFunc(tool.FuncConfig{Name: "duration", Effect: tool.EffectSafe}, func(_ context.Context, _ struct {
		Delay time.Duration `json:"delay"`
	}) (string, error) {
		return "done", nil
	})
	if err == nil {
		t.Fatal("signature published a duration without a default JSON codec")
	}
}
