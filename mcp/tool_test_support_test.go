package mcp_test

import (
	"context"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func prepareTestTool(executable toolcontract.Tool, arguments string) (toolcontract.Binding, toolcontract.Invocation, error) {
	binding, err := toolcontract.Bind(executable)
	if err != nil {
		return toolcontract.Binding{}, toolcontract.Invocation{}, err
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{
		ID: "test-call", Name: binding.Contract().Definition().Name, Arguments: arguments,
	})
	return binding, invocation, err
}

func invokeTestTool(ctx context.Context, executable toolcontract.Tool, arguments string) (chat.ToolOutput, error) {
	binding, invocation, err := prepareTestTool(executable, arguments)
	if err != nil {
		return chat.ToolOutput{}, err
	}
	return binding.Call(ctx, invocation)
}

func testOutputText(output chat.ToolOutput) string {
	text, _ := output.Text()
	return text
}
