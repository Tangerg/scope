package jsonschema_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"net"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func TestIPAddressSchemaAdmitsBothFamiliesAndEmptyEncoding(t *testing.T) {
	shape, err := jsonschema.For[net.IP]()
	if err != nil {
		t.Fatal(err)
	}
	for name, address := range map[string]net.IP{
		"IPv4":        net.ParseIP("192.0.2.7"),
		"IPv6":        net.ParseIP("2001:db8::7"),
		"mapped IPv4": net.ParseIP("::ffff:192.0.2.7"),
		"nil":         nil,
		"empty":       {},
	} {
		t.Run(name, func(t *testing.T) {
			wire, encodeErr := jsonv2.Marshal(address)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if validationErr := shape.Validate(wire); validationErr != nil {
				t.Fatalf("schema rejected net.IP encoding %s: %v", wire, validationErr)
			}
			var decoded net.IP
			if decodeErr := jsonv2.Unmarshal(wire, &decoded); decodeErr != nil || !address.Equal(decoded) {
				t.Fatalf("address did not round trip: %s, %v", decoded, decodeErr)
			}
		})
	}
	for _, invalid := range []string{`"192.0.2.999"`, `"2001:db8::zz"`, `"host.example"`, `"2001:db8::7%eth0"`, `7`, `[]`, `null`} {
		if validationErr := shape.Validate([]byte(invalid)); !errors.Is(validationErr, jsonschema.ErrValidation) {
			t.Fatalf("invalid address admitted: %s, %v", invalid, validationErr)
		}
	}
}

func TestIPAddressToolAdmitsAndPreservesBothFamilies(t *testing.T) {
	type input struct {
		Address net.IP `json:"address"`
	}
	function, err := tool.NewFunc(tool.FuncConfig{Name: "address", Effect: tool.EffectSafe}, func(_ context.Context, value input) (input, error) {
		return value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(function)
	if err != nil {
		t.Fatal(err)
	}
	for _, arguments := range []string{`{"address":"192.0.2.7"}`, `{"address":"2001:db8::7"}`, `{"address":""}`} {
		invocation, prepareErr := binding.Contract().Prepare(tool.Request{Name: "address", Arguments: arguments}, tool.Admission{})
		if prepareErr != nil {
			t.Fatalf("valid address rejected before call: %s, %v", arguments, prepareErr)
		}
		result, callErr := binding.Call(t.Context(), invocation)
		if callErr != nil || string(result.Output.Details) != arguments {
			t.Fatalf("address changed during tool round trip: %s, %v", result.Output.Details, callErr)
		}
	}
}
