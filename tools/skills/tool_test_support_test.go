package skills

import (
	"context"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

func invokeTestTool(ctx context.Context, executable toolcontract.Tool, arguments string) (toolcontract.Output, error) {
	binding, err := toolcontract.Bind(executable)
	if err != nil {
		return toolcontract.Output{}, err
	}
	invocation, err := binding.Contract().Prepare(toolcontract.Request{Name: binding.Contract().Definition().Name, Arguments: arguments}, toolcontract.Admission{})
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
