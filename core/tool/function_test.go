package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type addInput struct {
	A int `json:"a"`
	B int `json:"b"`
}

type contextKey struct{}

func TestNewFuncBuildsImmutableTypedTool(t *testing.T) {
	function, err := tool.NewFunc(tool.FuncConfig{Name: "add", Description: "add two integers"},
		func(ctx context.Context, input addInput) (int, error) {
			if got := ctx.Value(contextKey{}); got != "value" {
				t.Fatalf("context value = %v", got)
			}
			return input.A + input.B, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	definition := function.Definition()
	if definition.Name != "add" || definition.Description != "add two integers" {
		t.Fatalf("definition = %+v", definition)
	}
	if schema := string(definition.InputSchema); !strings.Contains(schema, `"a"`) || !strings.Contains(schema, `"b"`) {
		t.Fatalf("schema = %s", schema)
	}
	definition.InputSchema[0] = '['
	if function.Definition().InputSchema[0] != '{' {
		t.Fatal("mutating returned definition changed the function tool")
	}

	ctx := context.WithValue(t.Context(), contextKey{}, "value")
	result, err := call(t, ctx, function, `{"a":2,"b":3}`)
	if text, ok := result.Text(); err != nil || !ok || text != "5" {
		t.Fatalf("Call = %#v, %v", result, err)
	}
	if _, err := call(t, t.Context(), function, `{"extra":true}`); err == nil {
		t.Fatal("Call accepted an unknown field")
	}
}

func TestNewFuncRejectsInvalidConstruction(t *testing.T) {
	valid := func(context.Context, struct{}) (string, error) { return "", nil }
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "missing name", run: func() error {
			_, err := tool.NewFunc(tool.FuncConfig{}, valid)
			return err
		}},
		{name: "whitespace name", run: func() error {
			_, err := tool.NewFunc(tool.FuncConfig{Name: "bad name"}, valid)
			return err
		}},
		{name: "nil function", run: func() error {
			_, err := tool.NewFunc[struct{}, string](tool.FuncConfig{Name: "nil"}, nil)
			return err
		}},
		{name: "scalar input", run: func() error {
			_, err := tool.NewFunc(tool.FuncConfig{Name: "scalar"}, func(context.Context, string) (string, error) { return "", nil })
			return err
		}},
		{name: "interface input", run: func() error {
			_, err := tool.NewFunc(tool.FuncConfig{Name: "interface"}, func(context.Context, any) (string, error) { return "", nil })
			return err
		}},
		{name: "pointer chain input", run: func() error {
			_, err := tool.NewFunc(tool.FuncConfig{Name: "pointers"}, func(context.Context, **addInput) (string, error) { return "", nil })
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, tool.ErrInvalidTool) {
				t.Fatalf("error = %v, want ErrInvalidTool", err)
			}
		})
	}
}

func TestFuncZeroValueIsInvalidWithoutNilState(t *testing.T) {
	var function tool.Func[struct{}, string]
	if definition := function.Definition(); definition.Name != "" || definition.InputSchema != nil {
		t.Fatalf("zero Definition = %#v", definition)
	}
	if _, err := function.Call(t.Context(), tool.Invocation{}); !errors.Is(err, tool.ErrInvalidTool) {
		t.Fatalf("zero Call error = %v, want ErrInvalidTool", err)
	}
}

func TestFuncDecodesStrictObjectArguments(t *testing.T) {
	type optionalInput struct {
		Value string `json:"value,omitempty"`
	}
	function, err := tool.NewFunc(tool.FuncConfig{Name: "optional"},
		func(_ context.Context, input *optionalInput) (string, error) {
			if input == nil {
				t.Fatal("pointer input was not allocated")
			}
			return input.Value, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, arguments := range []string{"", "  ", "{}"} {
		result, err := call(t, t.Context(), function, arguments)
		if text, ok := result.Text(); err != nil || !ok || text != "" {
			t.Fatalf("Call(%q) = %#v, %v", arguments, result, err)
		}
	}
	for _, arguments := range []string{`null`, `[]`, `"text"`, `{"unknown":true}`, `{"value":"first","value":"second"}`, `{} {}`, `{`} {
		if _, err := call(t, t.Context(), function, arguments); err == nil {
			t.Errorf("Call(%q) succeeded, want decode error", arguments)
		}
	}
}

func TestFuncValidatesArgumentsAgainstDerivedSchema(t *testing.T) {
	type constrainedInput struct {
		Query string `json:"query" jsonschema:"minLength=2,maxLength=5,pattern=^[a-z]+$"`
		Limit int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=3"`
	}
	function, err := tool.NewFunc(tool.FuncConfig{Name: "constrained"},
		func(_ context.Context, input constrainedInput) (string, error) { return input.Query, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := call(t, t.Context(), function, `{"query":"valid","limit":3}`); err != nil || mustText(t, got) != "valid" {
		t.Fatalf("valid Call = %#v, %v", got, err)
	}
	for _, arguments := range []string{
		`{}`,
		`{"query":"x"}`,
		`{"query":"TOO"}`,
		`{"query":"valid","limit":4}`,
	} {
		if _, err := call(t, t.Context(), function, arguments); err == nil || !errors.Is(err, tool.ErrInvalidInvocation) {
			t.Errorf("Call(%s) error = %v, want schema violation", arguments, err)
		}
	}
}

func TestFuncResultEncodingAndErrorIdentity(t *testing.T) {
	t.Run("defined string is verbatim", func(t *testing.T) {
		type text string
		function, err := tool.NewFunc(tool.FuncConfig{Name: "text"}, func(context.Context, struct{}) (text, error) {
			return "plain", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := call(t, t.Context(), function, `{}`); err != nil || mustText(t, got) != "plain" {
			t.Fatalf("Call = %#v, %v", got, err)
		}
	})

	t.Run("composite and raw JSON use JSON encoding", func(t *testing.T) {
		type output struct {
			OK bool `json:"ok"`
		}
		composite, err := tool.NewFunc(tool.FuncConfig{Name: "composite"}, func(context.Context, struct{}) (output, error) {
			return output{OK: true}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, callErr := call(t, t.Context(), composite, `{}`); callErr != nil || string(got.Details) != `{"ok":true}` || len(got.Content) != 0 {
			t.Fatalf("composite Call = %#v, %v", got, callErr)
		}

		raw, err := tool.NewFunc(tool.FuncConfig{Name: "raw"}, func(context.Context, struct{}) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := call(t, t.Context(), raw, `{}`); err != nil || string(got.Details) != `{"ok":true}` {
			t.Fatalf("raw Call = %#v, %v", got, err)
		}
	})

	t.Run("empty output stays empty", func(t *testing.T) {
		function, err := tool.NewFunc(tool.FuncConfig{Name: "empty"}, func(context.Context, struct{}) (string, error) {
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := call(t, t.Context(), function, `{}`); err != nil || mustText(t, got) != "" {
			t.Fatalf("Call = %#v, %v", got, err)
		}
	})

	t.Run("function error is preserved", func(t *testing.T) {
		want := errors.New("failed")
		function, err := tool.NewFunc(tool.FuncConfig{Name: "error"}, func(context.Context, struct{}) (string, error) {
			return "partial", want
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := call(t, t.Context(), function, `{}`); len(got.Content) != 0 || len(got.Details) != 0 || !errors.Is(err, want) {
			t.Fatalf("Call = %#v, %v", got, err)
		}
	})

	t.Run("encoding error is wrapped", func(t *testing.T) {
		function, err := tool.NewFunc(tool.FuncConfig{Name: "channel"}, func(context.Context, struct{}) (chan int, error) {
			return make(chan int), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := call(t, t.Context(), function, `{}`); err == nil || !strings.Contains(err.Error(), "encode function result") {
			t.Fatalf("Call error = %v", err)
		}
	})
}

func TestFuncConcurrentCalls(t *testing.T) {
	function, err := tool.NewFunc(tool.FuncConfig{Name: "concurrent"}, func(_ context.Context, input addInput) (int, error) {
		return input.A + input.B, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	for range 32 {
		wait.Go(func() {
			if got, err := call(t, t.Context(), function, `{"a":1,"b":2}`); err != nil || mustText(t, got) != "3" {
				t.Errorf("Call = %#v, %v", got, err)
			}
		})
	}
	wait.Wait()
}

func call(t *testing.T, ctx context.Context, executable tool.Tool, arguments string) (chat.ToolOutput, error) {
	t.Helper()
	binding, err := tool.Bind(executable)
	if err != nil {
		return chat.ToolOutput{}, err
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{
		ID: "call", Name: binding.Contract().Definition().Name, Arguments: arguments,
	})
	if err != nil {
		return chat.ToolOutput{}, err
	}
	return binding.Call(ctx, invocation)
}

func mustText(t *testing.T, output chat.ToolOutput) string {
	t.Helper()
	text, ok := output.Text()
	if !ok {
		t.Fatalf("output %#v is not text-only", output)
	}
	return text
}
