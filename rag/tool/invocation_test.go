package tool_test

import (
	"context"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func invokeTestTool(ctx context.Context, executable tool.Tool, arguments string) (chat.ToolOutput, error) {
	binding, err := tool.Bind(executable)
	if err != nil {
		return chat.ToolOutput{}, err
	}
	invocation, err := binding.Prepare(chat.ToolCall{
		ID: "test-call", Name: binding.Definition().Name, Arguments: arguments,
	})
	if err != nil {
		return chat.ToolOutput{}, err
	}
	return binding.Call(ctx, invocation)
}
