package tool_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func TestOutputValidationAdmitsOnlyAbsentOrCompleteDetails(t *testing.T) {
	for name, details := range map[string]json.RawMessage{
		"absent":        nil,
		"null":          []byte(`null`),
		"object":        []byte(`{}`),
		"array":         []byte(`[]`),
		"exact integer": []byte(`9007199254740993`),
	} {
		t.Run(name, func(t *testing.T) {
			output := tool.Output{Details: details}
			if err := output.Validate(); err != nil {
				t.Fatal(err)
			}
			wire, err := jsonv2.Marshal(output)
			if err != nil {
				t.Fatalf("admitted output cannot be encoded: %v", err)
			}
			var decoded tool.Output
			if err := jsonv2.Unmarshal(wire, &decoded); err != nil || string(decoded.Details) != string(details) || (decoded.Details == nil) != (details == nil) {
				t.Fatalf("output changed during round trip: %s, %v", wire, err)
			}
		})
	}
	for name, details := range map[string]json.RawMessage{
		"empty":               {},
		"empty with capacity": make([]byte, 0, 8),
		"whitespace":          []byte(" "),
		"truncated":           []byte(`{`),
		"duplicate member":    []byte(`{"id":1,"id":2}`),
	} {
		t.Run(name, func(t *testing.T) {
			output := tool.Output{Details: details}
			if err := output.Validate(); !errors.Is(err, tool.ErrInvalidOutput) {
				t.Fatalf("invalid details admitted: %v", err)
			}
			if _, err := jsonv2.Marshal(output); !errors.Is(err, tool.ErrInvalidOutput) {
				t.Fatalf("encoding bypassed output validation: %v", err)
			}
			if err := output.Clone().Validate(); !errors.Is(err, tool.ErrInvalidOutput) {
				t.Fatalf("clone concealed invalid details: %v", err)
			}
		})
	}
}

func TestToolBindingRejectsPresentEmptyDetailsBeforeReturning(t *testing.T) {
	signature, err := tool.NewSignature[struct{}, tool.Output]()
	if err != nil {
		t.Fatal(err)
	}
	if _, encodeErr := signature.Encode(tool.Output{Details: json.RawMessage{}}); !errors.Is(encodeErr, tool.ErrInvalidOutput) {
		t.Fatalf("signature admitted invalid details: %v", encodeErr)
	}
	function, err := tool.NewFunc(tool.FuncConfig{Name: "empty_details", Effect: tool.EffectSafe}, func(context.Context, struct{}) (tool.Output, error) {
		return tool.Output{Details: json.RawMessage{}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, t.Context(), function, `{}`); err == nil {
		t.Fatalf("invalid details escaped the tool boundary: %v", err)
	}
}
