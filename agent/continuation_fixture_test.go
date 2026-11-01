package agent_test

import (
	"context"
	"encoding/json"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

type continuingTool struct {
	definition tool.Definition
	call       func(context.Context, tool.Invocation) (tool.Result, error)
}

func newContinuingTool(config tool.FuncConfig, call func(context.Context, tool.Invocation) (tool.Result, error)) (continuingTool, error) {
	definition := tool.Definition{Name: config.Name, Description: config.Description, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
	if err := definition.Validate(); err != nil {
		return continuingTool{}, err
	}
	return continuingTool{definition: definition, call: call}, nil
}
func (c continuingTool) Definition() tool.Definition { return c.definition.Clone() }
func (c continuingTool) Call(ctx context.Context, input tool.Invocation) (tool.Result, error) {
	return c.call(ctx, input)
}
func newInputRequest(prompt, schema, state json.RawMessage) (tool.InputRequest, error) {
	parsed, err := jsonschema.Parse(schema)
	if err != nil {
		return tool.InputRequest{}, err
	}
	return tool.NewInputRequest(tool.InputRequestConfig{Prompt: prompt, ResponseSchema: parsed, ContinuationState: state})
}
func waitingInput(prompt, schema, state json.RawMessage) (tool.Result, error) {
	request, err := newInputRequest(prompt, schema, state)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{InputRequest: &request}, nil
}
