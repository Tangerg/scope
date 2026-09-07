package workflow

import (
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

// SwitchSelector is a bounded, deterministic, side-effect-free case selector.
// It returns the exact SwitchCase ID to invoke for the current value.
type SwitchSelector[I any] func(input I) (caseID string, err error)

// SwitchCase declares one exact child Deployment for a selected case.
type SwitchCase struct {
	// ID is unique within this Switch Stage and stable across restoration.
	ID string

	// Deployment is the exact child behavior binding for this case.
	Deployment agent.Deployment

	// Budget is permanently allocated from the parent when selected.
	Budget agent.Budget

	// Capabilities is the attenuated authority set granted to the child.
	Capabilities agent.CapabilitySet
}

// SwitchConfig declares one pure selection function and a closed case set.
type SwitchConfig[I any] struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	// Select chooses one case without performing external work.
	Select SwitchSelector[I]

	// Cases is a non-empty list in stable declaration order.
	Cases []SwitchCase
}

type switchCase struct {
	id      string
	binding childBinding
}

type switchStage struct {
	selectCase func(json.RawMessage) (string, error)
	cases      []switchCase
}

func (s switchStage) valid() bool {
	if s.selectCase == nil || len(s.cases) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(s.cases))
	for _, candidate := range s.cases {
		if !validStageID(candidate.id) || !candidate.binding.valid() {
			return false
		}
		if _, duplicate := seen[candidate.id]; duplicate {
			return false
		}
		seen[candidate.id] = struct{}{}
	}
	return true
}

func (c childBinding) valid() bool {
	return c.deploymentRef.Valid() && c.budget.Valid() && c.capabilities.Valid()
}

// Switch constructs one selected managed child-Process Stage. Every case must
// accept the same I schema and produce one exactly matching output schema.
func Switch[I any](config SwitchConfig[I]) (Stage, error) {
	if !validStageID(config.ID) || config.Select == nil || len(config.Cases) == 0 {
		return Stage{}, ErrInvalidStage
	}
	inputSchema, err := agent.SchemaFor[I]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Switch %q input schema: %w", ErrInvalidStage, config.ID, err)
	}
	cases := make([]switchCase, 0, len(config.Cases))
	indices := make(map[string]int, len(config.Cases))
	var outputSchema agent.Schema
	for index, candidate := range config.Cases {
		if !validStageID(candidate.ID) || !candidate.Deployment.Valid() ||
			!candidate.Budget.Valid() || !candidate.Capabilities.Valid() {
			return Stage{}, fmt.Errorf("%w: Switch %q Cases[%d]", ErrInvalidStage, config.ID, index)
		}
		if _, duplicate := indices[candidate.ID]; duplicate {
			return Stage{}, fmt.Errorf("%w: Switch %q has duplicate case %q", ErrInvalidStage, config.ID, candidate.ID)
		}
		descriptor := candidate.Deployment.Descriptor()
		if !schemasEqual(inputSchema, descriptor.InputSchema()) {
			return Stage{}, fmt.Errorf("%w: Switch %q case %q input schema mismatch", ErrInvalidStage, config.ID, candidate.ID)
		}
		if index == 0 {
			outputSchema = descriptor.OutputSchema()
		} else if !schemasEqual(outputSchema, descriptor.OutputSchema()) {
			return Stage{}, fmt.Errorf("%w: Switch %q case %q output schema mismatch", ErrInvalidStage, config.ID, candidate.ID)
		}
		indices[candidate.ID] = index
		cases = append(cases, switchCase{
			id: candidate.ID,
			binding: childBinding{
				deploymentRef: candidate.Deployment.DeploymentRef(), budget: candidate.Budget,
				capabilities: candidate.Capabilities,
			},
		})
	}
	selector := config.Select
	selectCase := func(raw json.RawMessage) (string, error) {
		input, err := agent.ParseInput(raw)
		if err != nil {
			return "", err
		}
		if validateInputErr := inputSchema.ValidateInput(input); validateInputErr != nil {
			return "", validateInputErr
		}
		decoded, err := input.Decode[I]()
		if err != nil {
			return "", err
		}
		selected, err := selector(decoded)
		if err != nil {
			return "", fmt.Errorf("Switch %q selector: %w", config.ID, err)
		}
		if _, found := indices[selected]; !found {
			return "", unknownSwitchCaseError{id: selected}
		}
		return selected, nil
	}
	return Stage{
		id: config.ID, kind: StageKindSwitch,
		inputSchema: inputSchema, outputSchema: outputSchema,
		switcher: switchStage{selectCase: selectCase, cases: cases},
	}, nil
}

func (s switchStage) binding(caseID string) (childBinding, bool) {
	for _, candidate := range s.cases {
		if candidate.id == caseID {
			return candidate.binding, true
		}
	}
	return childBinding{}, false
}

type unknownSwitchCaseError struct{ id string }

func (u unknownSwitchCaseError) Error() string {
	return fmt.Sprintf("Switch selector returned undeclared case %q", u.id)
}
