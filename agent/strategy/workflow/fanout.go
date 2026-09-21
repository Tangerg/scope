package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

// fanoutSource supplies members and their inputs; window settlement and output
// handling belong to fanoutStage for both fixed branches and repeated items.
// Its unexported methods keep the Workflow operation set closed.
type fanoutSource interface {
	count(ctx context.Context, raw json.RawMessage) (uint32, error)
	// windowInputs rejects start beyond the source count and returns the
	// ordered window plus the total count. A start at count returns an empty window.
	windowInputs(ctx context.Context, raw json.RawMessage, start, windowSize uint32) (inputs []agent.Payload, count uint32, err error)
	member(index uint32) (fanoutMember, bool)
	topology(inputSchema, outputSchema agent.Schema) ([]BindingTopology, uint32)
}

type fanoutMember struct {
	id      string
	binding childBinding
}

type fanoutStage struct {
	source       fanoutSource
	windowSize   uint32
	outputSchema agent.Schema
	complete     func(context.Context, []json.RawMessage) (json.RawMessage, error)
}

type fanoutOutputDecoder struct {
	stageName  string
	stageID    string
	memberName string
	schema     agent.Schema
}

func (f fanoutOutputDecoder) decode[T any](ctx context.Context, encodedOutputs []json.RawMessage) ([]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make([]T, len(encodedOutputs))
	for index, encoded := range encodedOutputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		output, err := agent.ParsePayload(encoded)
		if err != nil {
			return nil, fmt.Errorf("%s %q %s %d output: %w", f.stageName, f.stageID, f.memberName, index, err)
		}
		if validateOutputErr := f.schema.Validate(output.JSON()); validateOutputErr != nil {
			return nil, fmt.Errorf("%s %q %s %d output contract: %w", f.stageName, f.stageID, f.memberName, index, validateOutputErr)
		}
		decoded, err := output.Decode[T]()
		if err != nil {
			return nil, fmt.Errorf("%s %q %s %d decode output: %w", f.stageName, f.stageID, f.memberName, index, err)
		}
		values[index] = decoded
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return values, nil
}
