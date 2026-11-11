package workflow

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

type fanoutSource interface {
	prepareInput(ctx context.Context, input run.Payload) (fanoutInput, error)
	member(index uint32) (fanoutMember, bool)
	topology(inputSchema, outputSchema jsonschema.Schema) ([]BindingTopology, uint32)
	bindings() []run.ChildBinding
}

// fanoutInput is an execution-local view of the current stage input. It cannot
// advance progress, and is discarded when the owning stage finishes.
type fanoutInput interface {
	count() uint32
	windowInputs(ctx context.Context, start, windowSize uint32) ([]run.Payload, error)
}

type fanoutMember struct {
	id      string
	binding run.ChildBinding
}

// fanoutWindowLen is how many of count members the window at start covers:
// windows tile the members in windowSize steps, and only the last is short.
func fanoutWindowLen(start, count, windowSize uint32) uint32 {
	return min(windowSize, count-start)
}

type fanoutStage struct {
	source     fanoutSource
	windowSize uint32
	// memberOutputSchema governs one branch or item, not the Stage result.
	memberOutputSchema jsonschema.Schema
	// complete receives the Stage input with the member outputs, so members
	// never carry the input forward themselves.
	complete func(ctx context.Context, input run.Payload, outputs []run.Payload) (run.Payload, error)
}

// validateMemberOutput admits a member output restored from opaque state. A
// live output needs no check: the Runtime held it to the member's Program,
// whose output schema construction requires to equal memberOutputSchema.
func (f fanoutStage) validateMemberOutput(output run.Payload) error {
	return f.memberOutputSchema.Validate(output.JSON())
}

type fanoutOutputs struct {
	kind         StageKind
	stageID      string
	resultSchema jsonschema.Schema
}

func (f fanoutOutputs) decode[T any](ctx context.Context, encodedOutputs []run.Payload) ([]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make([]T, len(encodedOutputs))
	for index, encoded := range encodedOutputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		decoded, err := encoded.Decode[T]()
		if err != nil {
			return nil, fmt.Errorf("%s %q %s %d decode output: %w", f.kind, f.stageID, f.kind.fanoutMemberNoun(), index, err)
		}
		values[index] = decoded
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func (f fanoutOutputs) encodeResult[O any](result O) (run.Payload, error) {
	erased, err := run.EncodePayload(result)
	if err != nil {
		return run.Payload{}, fmt.Errorf("%s %q encode result: %w", f.kind, f.stageID, err)
	}
	if err := f.resultSchema.Validate(erased.JSON()); err != nil {
		return run.Payload{}, fmt.Errorf("%s %q result contract: %w", f.kind, f.stageID, err)
	}
	return erased, nil
}
