package tool_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func Example() {
	type input struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	add, err := tool.NewFunc(tool.FuncConfig{
		Name:        "add",
		Description: "add two integers",
	}, func(_ context.Context, value input) (int, error) {
		return value.A + value.B, nil
	})
	if err != nil {
		panic(err)
	}
	registry, err := tool.NewRegistry(add)
	if err != nil {
		panic(err)
	}

	fmt.Println(registry.Definitions()[0].Name)
	binding, ok := registry.Resolve("add")
	if !ok {
		panic("missing add")
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "call-1", Name: "add", Arguments: `{"a":2,"b":3}`})
	if err != nil {
		panic(err)
	}
	result, err := binding.Call(context.Background(), invocation)
	if err != nil {
		panic(err)
	}
	text, _ := result.Text()
	fmt.Println(text)
	// Output:
	// add
	// 5
}

func ExampleNewFailure() {
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindRejected, Cause: errors.New("internal authorization diagnostic"),
		Output: chat.NewTextToolOutput("this operation is not permitted"),
	})
	if err != nil {
		panic(err)
	}
	wrapped := fmt.Errorf("tool call: %w", failure)
	result, found := errors.AsType[*tool.Failure](wrapped)
	if !found {
		panic("missing definite outcome")
	}
	text, _ := result.Output().Text()
	fmt.Println(result.Kind(), text)
	fmt.Println(errors.Is(wrapped, result.Cause()))
	// Output:
	// rejected this operation is not permitted
	// false
}

func ExampleNewGuard() {
	executable, err := tool.NewFunc(tool.FuncConfig{Name: "inspect"}, func(context.Context, struct{}) (string, error) {
		return "inspected", nil
	})
	if err != nil {
		panic(err)
	}
	guard, err := tool.NewGuard(tool.GuardConfig{
		Tool: executable,
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
			return false, nil
		}),
	})
	if err != nil {
		panic(err)
	}
	binding, err := tool.Bind(guard)
	if err != nil {
		panic(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`})
	if err != nil {
		panic(err)
	}
	_, err = binding.Call(context.Background(), invocation)
	failure, found := errors.AsType[*tool.Failure](err)
	if !found {
		panic(err)
	}
	fmt.Println(failure.Kind())
	// Output:
	// rejected
}
