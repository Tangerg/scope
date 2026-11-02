package interaction

import (
	"encoding/json"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

func testTurn(step uint64) run.Turn {
	process, _ := run.ParseProcessID("interaction-test")
	return run.Turn{Process: process, Step: step}
}
func newInputRequest(prompt, schema, state json.RawMessage) (tool.InputRequest, error) {
	contract, err := jsonschema.Parse(schema)
	if err != nil {
		return tool.InputRequest{}, err
	}
	return tool.NewInputRequest(tool.InputRequestConfig{Prompt: prompt, ResponseSchema: contract, ContinuationState: state})
}
