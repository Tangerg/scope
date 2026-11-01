package mcp_test

import (
	"context"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

func prepareTestTool(executable toolcontract.Tool, arguments string) (toolcontract.Binding, toolcontract.Invocation, error) {
	binding, err := toolcontract.Bind(executable)
	if err != nil {
		return toolcontract.Binding{}, toolcontract.Invocation{}, err
	}
	invocation, err := binding.Contract().Prepare(toolcontract.Request{Name: binding.Contract().Definition().Name, Arguments: arguments}, nil)
	return binding, invocation, err
}

func invokeTestTool(ctx context.Context, executable toolcontract.Tool, arguments string) (toolcontract.Output, error) {
	binding, invocation, err := prepareTestTool(executable, arguments)
	if err != nil {
		return toolcontract.Output{}, err
	}
	result, err := binding.Call(ctx, invocation)
	return result.Output, err
}

func testOutputText(output toolcontract.Output) string {
	text, _ := output.Text()
	return text
}
