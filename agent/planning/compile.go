package planning

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// Config binds a planner, its world sensor and executable actions. Compile's
// input type parameter supplies the schema of the opaque task input passed to
// the Sensor, ActionExecutors, and child input functions. Revision identifies
// their opaque settings, including cost functions and child input conversion.
// The compiler hashes the declared planning domain.
type Config struct {
	Name        string
	Description string
	Goal        Goal
	Actions     []ActionBinding
	Planner     Planner
	Sensor      Sensor
	// MaxActionAttempts bounds external Action attempts. Its zero value is
	// unlimited; a finite zero is rejected because Planning must admit an Action.
	// This is an execution limit, not a Planner path-length constraint: a
	// lowest-cost plan may exceed the remaining attempts and finish Exhausted
	// even when a more expensive shorter plan could reach the Goal.
	MaxActionAttempts run.Quota
	Revision          string
}

func Compile[I any](compiler *run.Compiler, cfg Config) (run.Program[I, Output], error) {
	var zero run.Program[I, Output]

	if cfg.Revision == "" {
		return zero, fmt.Errorf("%w: Revision is required", run.ErrInvalidProgram)
	}
	in, err := jsonschema.For[I]()
	if err != nil {
		return zero, err
	}
	declarations, executors, err := splitActionBindings(cfg.Actions)
	if err != nil {
		return zero, err
	}
	def, err := newDefinition(cfg, in, declarations)
	if err != nil {
		return zero, err
	}
	dispatch, err := newDispatcher(def, cfg.Sensor, executors)
	if err != nil {
		return zero, err
	}
	type configuredAction struct {
		Name, Description      string
		Preconditions, Effects []Condition
		Policy                 run.EffectPolicy
		Child                  *run.ProgramRef `json:"child,omitzero"`
		Budget                 run.Budget
		Capabilities           run.CapabilitySet
	}
	actions := make([]configuredAction, 0, len(def.bindings))
	for _, b := range def.bindings {
		var child *run.ProgramRef
		if b.delegatesToChild() {
			child = new(b.child.Program.ProgramRef())
		}
		a := b.action
		actions = append(actions, configuredAction{a.Name(), a.Description(), a.Preconditions(), a.Effects(), b.policy, child, b.child.Budget, b.child.Capabilities})
	}
	configuration := struct {
		Goal, Description string
		Conditions        []Condition
		Actions           []configuredAction
		Attempts          run.Quota
	}{def.goal.Name(), def.goal.Description(), def.goal.Conditions(), actions, def.maxActionAttempts}
	return run.Bind[I, Output](compiler, run.BindingConfig{
		Definition: def, Dispatcher: dispatch, Revision: cfg.Revision,
		Configuration: configuration,
	})
}

func splitActionBindings(bindings []ActionBinding) ([]actionDeclaration, []ActionExecutor, error) {
	declarations := make([]actionDeclaration, len(bindings))
	executors := make([]ActionExecutor, len(bindings))
	for index, binding := range bindings {
		if !binding.Valid() {
			return nil, nil, fmt.Errorf("%w: Actions[%d]", ErrInvalidConfig, index)
		}
		declarations[index], executors[index] = binding.declaration, binding.executor
	}
	return declarations, executors, nil
}
