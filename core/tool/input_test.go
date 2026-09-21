package tool_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type validatingTool struct {
	markedTool
	declare func() func([]byte) error
}

func (v validatingTool) InputValidator() func([]byte) error { return v.declare() }

func TestInputAdmissionFreezesDeclarationAndRejectsPanics(t *testing.T) {
	declarations := 0
	cause := errors.New("input is inadmissible")
	executable := validatingTool{declare: func() func([]byte) error {
		declarations++
		return func([]byte) error { return cause }
	}}
	binding, err := tool.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := binding.Contract().Prepare(chat.ToolCall{ID: "call", Name: "marked", Arguments: `{}`}); !errors.Is(err, tool.ErrInvalidInvocation) || !errors.Is(err, cause) {
			t.Fatalf("Prepare did not preserve the admission failure: %v", err)
		}
	}
	if declarations != 1 {
		t.Fatalf("declarations = %d, want 1", declarations)
	}
	for _, testCase := range []struct {
		name    string
		declare func() func([]byte) error
		bindErr bool
	}{
		{"declaration panic", func() func([]byte) error { panic("declaration failed") }, true},
		{"validation panic", func() func([]byte) error { return func([]byte) error { panic("validation failed") } }, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			binding, err := tool.Bind(validatingTool{declare: testCase.declare})
			if testCase.bindErr {
				if !errors.Is(err, tool.ErrInvalidTool) || !strings.Contains(err.Error(), "declaration failed") {
					t.Fatalf("Bind = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := binding.Contract().Prepare(chat.ToolCall{ID: "call", Name: "marked", Arguments: `{}`}); !errors.Is(err, tool.ErrInvalidInvocation) || !strings.Contains(err.Error(), "validation failed") {
				t.Fatalf("Prepare = %v", err)
			}
		})
	}
}

func TestInputValidationPanicPreservesDiagnosticCause(t *testing.T) {
	cause := errors.New("validator defect")
	for _, declaration := range []bool{false, true} {
		binding, err := tool.Bind(validatingTool{declare: func() func([]byte) error {
			if declaration {
				panic(cause)
			}
			return func([]byte) error { panic(cause) }
		}})
		if !declaration {
			if err != nil {
				t.Fatal(err)
			}
			_, err = binding.Contract().Prepare(chat.ToolCall{ID: "call", Name: "marked", Arguments: `{}`})
			if !errors.Is(err, tool.ErrInvalidInvocation) {
				t.Fatalf("lost rejection: %v", err)
			}
		}
		panicErr, ok := errors.AsType[*tool.InputValidationPanicError](err)
		if !ok || !errors.Is(panicErr.Unwrap(), cause) || !errors.Is(err, cause) {
			t.Fatalf("lost validator panic: %v", err)
		}
	}
}
