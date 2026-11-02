package coordination

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

type Policy interface{ coordinationPolicy() }

type InputGate struct {
	RequestSchema, AnswerSchema jsonschema.Schema
	MaxAnswerBytes              int
}

func (InputGate) coordinationPolicy() {}

type FirstSuccess struct {
	MaxCandidates uint32
	Accept        SuccessPredicate
	Revision      string
}

func (FirstSuccess) coordinationPolicy() {}

type Deadline struct{}

func (Deadline) coordinationPolicy() {}

type Config struct {
	Name, Description string
	Policy            Policy
}

func Compile[I, O any](compiler *run.Compiler, config Config) (run.Program[I, O], error) {
	var binding run.BindingConfig
	switch policy := config.Policy.(type) {
	case InputGate:
		definition, err := newInputGate(config.Name, config.Description, policy)
		if err != nil {
			return run.Program[I, O]{}, err
		}
		binding = run.BindingConfig{Definition: definition, Configuration: policy}
	case FirstSuccess:
		definition, err := newFirstSuccess(config.Name, config.Description, policy)
		if err != nil {
			return run.Program[I, O]{}, err
		}
		binding = run.BindingConfig{Definition: definition, Revision: policy.Revision, Configuration: struct{ MaxCandidates uint32 }{policy.MaxCandidates}}
	case Deadline:
		definition, err := newDeadline(config.Name, config.Description)
		if err != nil {
			return run.Program[I, O]{}, err
		}
		binding = run.BindingConfig{Definition: definition, Dispatcher: deadlineDispatcher{}, Configuration: policy}
	default:
		return run.Program[I, O]{}, fmt.Errorf("%w: a coordination policy is required", ErrInvalidConfig)
	}
	return run.Bind[I, O](compiler, binding)
}
