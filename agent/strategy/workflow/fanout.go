package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

type fanoutSource interface {
	count(ctx context.Context, raw json.RawMessage) (uint32, error)
	// windowInputs rejects start beyond the source count and returns the
	// ordered window plus the total count. A start at count returns an empty window.
	windowInputs(ctx context.Context, raw json.RawMessage, start, windowSize uint32) (inputs []agent.Payload, count uint32, err error)
	member(index uint32) (fanoutMember, bool)
	topology(inputSchema, outputSchema agent.Schema) ([]BindingTopology, uint32)
	bindings() []childBinding
}

type fanoutMember struct {
	id      string
	binding childBinding
}

type fanoutStage struct {
	source       fanoutSource
	windowSize   uint32
	outputSchema agent.Schema
	// complete receives the Stage input with the member outputs, so members
	// never carry the input forward themselves.
	complete func(ctx context.Context, input json.RawMessage, outputs []json.RawMessage) (json.RawMessage, error)
}

type fanoutOutputs struct {
	stageName    string
	stageID      string
	memberName   string
	memberSchema agent.Schema
	resultSchema agent.Schema
}

func (f fanoutOutputs) decode[T any](ctx context.Context, encodedOutputs []json.RawMessage) ([]T, error) {
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
		if validateOutputErr := f.memberSchema.Validate(output.JSON()); validateOutputErr != nil {
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

func (f fanoutOutputs) encodeResult[O any](result O) (json.RawMessage, error) {
	erased, err := agent.EncodePayload(result)
	if err != nil {
		return nil, fmt.Errorf("%s %q encode result: %w", f.stageName, f.stageID, err)
	}
	if err := f.resultSchema.Validate(erased.JSON()); err != nil {
		return nil, fmt.Errorf("%s %q result contract: %w", f.stageName, f.stageID, err)
	}
	return erased.JSON(), nil
}
