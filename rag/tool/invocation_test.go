package tool_test

import (
	"context"

	"github.com/Tangerg/scope/core/tool"
)

func invokeTestTool(ctx context.Context, executable tool.Tool, arguments string) (tool.Output, error) {
	binding, err := tool.Bind(executable)
	if err != nil {
		return tool.Output{}, err
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: binding.Contract().Definition().Name, Arguments: arguments}, nil)
	if err != nil {
		return tool.Output{}, err
	}
	result, err := binding.Call(ctx, invocation)
	return result.Output, err
}
