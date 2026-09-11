package tool_test

import (
	"context"
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
