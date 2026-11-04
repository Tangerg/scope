package tool_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/tool"
)

func TestBindingEnforcesItsFrozenOutputSchemaAfterMiddleware(t *testing.T) {
	type response struct {
		Count int `json:"count"`
	}
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "count"}, func(context.Context, struct{}) (response, error) { return response{Count: 2}, nil })
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(executable, func(next tool.Caller) tool.Caller {
		return tool.CallerFunc(func(ctx context.Context, admitted tool.Invocation) (tool.Result, error) {
			result, callErr := next.Call(ctx, admitted)
			if callErr == nil {
				result.Output.Details = []byte(`{"count":"wrong"}`)
			}
			return result, callErr
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := binding.Contract().Definition()
	if len(definition.OutputSchema) == 0 {
		t.Fatal("typed result lost its output contract")
	}
	definition.OutputSchema[0] = '['
	admitted, err := binding.Contract().Prepare(tool.Request{Name: "count"}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Call(t.Context(), admitted); !errors.Is(err, tool.ErrInvalidResult) {
		t.Fatalf("invalid structured result escaped: %v", err)
	}
}

func TestSignaturePreservesRawOutputInsteadOfEncodingItsWrapper(t *testing.T) {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "raw"}, func(context.Context, struct{}) (tool.Output, error) {
		return tool.Output{Content: []content.Block{{Kind: content.Text, Text: "visible"}}, Details: []byte(`[1,null]`)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executable.Definition().OutputSchema) != 0 {
		t.Fatal("raw output advertised the Go wrapper as its Details schema")
	}
	output, err := call(t, t.Context(), executable, `{}`)
	if err != nil || string(output.Details) != `[1,null]` || mustText(t, output) != "visible" {
		t.Fatalf("output was re-encoded: %#v, %v", output, err)
	}
}

func TestSignatureTextDoesNotAdvertiseAStructuredOutputItDoesNotProduce(t *testing.T) {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "text"}, func(context.Context, struct{}) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(executable.Definition().OutputSchema) != 0 {
		t.Fatal("text-only codec advertised a Details contract")
	}
	output, err := call(t, t.Context(), executable, `{}`)
	if err != nil || len(output.Details) != 0 || len(output.Content) != 0 {
		t.Fatalf("empty text changed meaning: %#v, %v", output, err)
	}
}

func TestZeroSignatureCannotAdmitOrEncodeValues(t *testing.T) {
	var signature tool.Signature[struct{}, int]
	if _, err := signature.Decode(tool.Invocation{}); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("zero signature decoded a value: %v", err)
	}
	if _, err := signature.Encode(7); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("zero signature encoded a value: %v", err)
	}
}

func TestInterfaceOutputAlwaysUsesItsDeclaredJSONCodec(t *testing.T) {
	for _, sample := range []struct {
		value any
		wire  string
	}{
		{value: "text", wire: `"text"`},
		{value: nil, wire: `null`},
		{value: []int{1, 2}, wire: `[1,2]`},
	} {
		executable, err := tool.NewFunc(tool.FuncConfig{Name: "dynamic"}, func(context.Context, struct{}) (any, error) {
			return sample.value, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(executable.Definition().OutputSchema) == 0 {
			t.Fatal("interface output lost its JSON contract")
		}
		output, err := call(t, t.Context(), executable, `{}`)
		if err != nil || string(output.Details) != sample.wire {
			t.Fatalf("runtime type changed the declared codec: output=%s error=%v", output.Details, err)
		}
	}
}

var errBlankQuery = errors.New("query must not be blank")

type valueValidatedInput struct {
	Query string `json:"query"`
}

func (v valueValidatedInput) Validate() error {
	if v.Query == "" {
		return errBlankQuery
	}
	return nil
}

type pointerValidatedInput struct {
	Query string `json:"query"`
}

func (p *pointerValidatedInput) Validate() error {
	if p.Query == "" {
		return errBlankQuery
	}
	return nil
}

func TestSignatureAdmitsTypedInputThroughItsValidate(t *testing.T) {
	for name, validate := range map[string]func([]byte) error{
		"value receiver":   mustSignature[valueValidatedInput](t).InputValidator(),
		"pointer receiver": mustSignature[pointerValidatedInput](t).InputValidator(),
		"pointer input":    mustSignature[*valueValidatedInput](t).InputValidator(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate([]byte(`{"query":"go"}`)); err != nil {
				t.Fatalf("valid input rejected: %v", err)
			}
			if err := validate([]byte(`{"query":""}`)); !errors.Is(err, errBlankQuery) {
				t.Fatalf("domain rule bypassed: %v", err)
			}
		})
	}
	if err := mustSignature[*valueValidatedInput](t).InputValidator()([]byte(`null`)); !errors.Is(err, tool.ErrInvalidRequest) {
		t.Fatalf("null pointer input admitted: %v", err)
	}
}

func mustSignature[In any](t *testing.T) tool.Signature[In, string] {
	t.Helper()
	signature, err := tool.NewSignature[In, string]()
	if err != nil {
		t.Fatal(err)
	}
	return signature
}
