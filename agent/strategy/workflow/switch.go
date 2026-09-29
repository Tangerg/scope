package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

// SwitchSelector is a bounded, deterministic, side-effect-free case selector.
// It must honor ctx cancellation and returns the exact SwitchCase ID to invoke
// for the current value. Context is not a source of domain input.
type SwitchSelector[I any] func(ctx context.Context, input I) (caseID string, err error)

type SwitchCase struct {
	// ID is unique within this Switch Stage and stable across restoration.
	ID string

	Deployment agent.Deployment

	// Budget is permanently allocated from the parent when selected.
	Budget agent.Budget

	Capabilities agent.CapabilitySet
}

type SwitchConfig[I any] struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	Select SwitchSelector[I]

	// Cases is a non-empty list in stable declaration order.
	Cases []SwitchCase
}

type switchCase struct {
	id      string
	binding childBinding
}

type switchStage struct {
	selectCase func(context.Context, json.RawMessage) (string, error)
	cases      []switchCase
}

// Switch constructs one selected managed child-Process Stage. Every case must
// accept the same I schema and produce one exactly matching output schema.
func Switch[I any](config SwitchConfig[I]) (Stage, error) {
	if !agent.ValidQualifiedName(config.ID) || config.Select == nil || len(config.Cases) == 0 {
		return Stage{}, ErrInvalidStage
	}
	inputSchema, err := agent.SchemaFor[I]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Switch %q input schema: %w", ErrInvalidStage, config.ID, err)
	}
	cases, outputSchema, err := newSwitchCases(config.ID, inputSchema, config.Cases)
	if err != nil {
		return Stage{}, err
	}
	selector := config.Select
	selectCase := func(ctx context.Context, raw json.RawMessage) (string, error) {
		input, err := agent.ParsePayload(raw)
		if err != nil {
			return "", err
		}
		if validateInputErr := inputSchema.Validate(input.JSON()); validateInputErr != nil {
			return "", validateInputErr
		}
		decoded, err := input.Decode[I]()
		if err != nil {
			return "", err
		}
		selected, err := selector(ctx, decoded)
		if err != nil {
			return "", fmt.Errorf("Switch %q selector: %w", config.ID, err)
		}
		return selected, nil
	}
	return Stage{
		id: config.ID, kind: StageKindSwitch,
		inputSchema: inputSchema, outputSchema: outputSchema,
		switcher: switchStage{selectCase: selectCase, cases: cases},
	}, nil
}

func newSwitchCases(stageID string, inputSchema agent.Schema, declared []SwitchCase) ([]switchCase, agent.Schema, error) {
	cases := make([]switchCase, 0, len(declared))
	seen := make(map[string]struct{}, len(declared))
	var outputSchema agent.Schema
	for index, candidate := range declared {
		binding, valid := newChildBinding(candidate.Deployment, candidate.Budget, candidate.Capabilities)
		if !agent.ValidQualifiedName(candidate.ID) || !valid {
			return nil, agent.Schema{}, fmt.Errorf("%w: Switch %q Cases[%d]", ErrInvalidStage, stageID, index)
		}
		if _, duplicate := seen[candidate.ID]; duplicate {
			return nil, agent.Schema{}, fmt.Errorf("%w: Switch %q has duplicate case %q", ErrInvalidStage, stageID, candidate.ID)
		}
		seen[candidate.ID] = struct{}{}
		descriptor := candidate.Deployment.Descriptor()
		if !schemasEqual(inputSchema, descriptor.InputSchema()) {
			return nil, agent.Schema{}, fmt.Errorf("%w: Switch %q case %q input schema mismatch", ErrInvalidStage, stageID, candidate.ID)
		}
		if index == 0 {
			outputSchema = descriptor.OutputSchema()
		} else if !schemasEqual(outputSchema, descriptor.OutputSchema()) {
			return nil, agent.Schema{}, fmt.Errorf("%w: Switch %q case %q output schema mismatch", ErrInvalidStage, stageID, candidate.ID)
		}
		cases = append(cases, switchCase{id: candidate.ID, binding: binding})
	}
	return cases, outputSchema, nil
}

func (s switchStage) binding(caseID string) (childBinding, bool) {
	for _, candidate := range s.cases {
		if candidate.id == caseID {
			return candidate.binding, true
		}
	}
	return childBinding{}, false
}
