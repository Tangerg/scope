package workflow

import (
	"errors"

	"github.com/Tangerg/scope/agent/run"
)

// Stage IDs of an adapted Program. They are fixed so the topology of every
// adaptation reads the same and restoration never depends on caller naming.
const (
	adaptInputStage  = "input"
	adaptChildStage  = "child"
	adaptOutputStage = "output"
)

// AdaptConfig names an adapted Program and the authority its child receives.
// Revision and Configuration identify the two mapping callbacks exactly as
// [Config] does for any Transform.
type AdaptConfig struct {
	Name          string
	Description   string
	Revision      string
	Configuration any

	// Budget is permanently allocated from the parent when the child starts.
	Budget       run.Budget
	Capabilities run.CapabilitySet
}

// Adapt fits child to another role's ports: input maps I to the child's input,
// output maps the child's output to O. The result is an ordinary Workflow of
// one Transform, one Call, and one Transform, so it adds no execution
// semantics of its own.
func Adapt[I, A, B, O any](
	compiler *run.Compiler,
	config AdaptConfig,
	child run.Program[A, B],
	input TransformFunc[I, A],
	output TransformFunc[B, O],
) (run.Program[I, O], error) {
	inputStage, inputErr := Transform(adaptInputStage, input)
	childStage, childErr := Call(CallConfig{ID: adaptChildStage, Program: child.Erase(), Budget: config.Budget, Capabilities: config.Capabilities})
	outputStage, outputErr := Transform(adaptOutputStage, output)
	if err := errors.Join(inputErr, childErr, outputErr); err != nil {
		return run.Program[I, O]{}, err
	}
	return Compile[I, O](compiler, Config{
		Name: config.Name, Description: config.Description,
		Stages:   []Stage{inputStage, childStage, outputStage},
		Revision: config.Revision, Configuration: config.Configuration,
	})
}
