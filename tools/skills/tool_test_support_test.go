package skills

import (
	"context"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func invokeTestTool(ctx context.Context, executable toolcontract.Tool, arguments string) (chat.ToolOutput, error) {
	binding, err := toolcontract.Bind(executable)
	if err != nil {
		return chat.ToolOutput{}, err
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{
		ID: "test-call", Name: binding.Contract().Definition().Name, Arguments: arguments,
	}, nil)
	if err != nil {
		return chat.ToolOutput{}, err
	}
	result, err := binding.Call(ctx, invocation)
	return result.Output, err
}

func testOutputText(output chat.ToolOutput) string {
	text, _ := output.Text()
	return text
}
