package planning

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// Config binds a planner, its world sensor and executable actions.
// Revision identifies their opaque settings, including cost functions
// and child input conversion. The compiler hashes the declared planning domain.
type Config struct {
	Name        string
	Description string
	Goal        Goal
	Actions     []ActionBinding
	Planner     Planner
	Sensor      Sensor
	// MaxActionAttempts is unlimited by default; a finite zero cannot admit an action.
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
	def, err := newDefinition(definitionConfig{Name: cfg.Name, Description: cfg.Description, InputSchema: in, Goal: cfg.Goal, Actions: cfg.Actions, Planner: cfg.Planner, MaxActionAttempts: cfg.MaxActionAttempts})
	if err != nil {
		return zero, err
	}
	dispatch, err := newDispatcher(def, cfg.Sensor)
	if err != nil {
		return zero, err
	}
	type actionFact struct {
		Name, Description      string
		Preconditions, Effects []Condition
		Policy                 run.EffectPolicy
		Child                  *run.ProgramRef `json:"child,omitzero"`
		Budget                 run.Budget
		Capabilities           run.CapabilitySet
	}
	actions := make([]actionFact, 0, len(def.bindings))
	for _, b := range def.bindings {
		var child *run.ProgramRef
		if b.delegatesToChild() {
			child = new(b.child.Program.ProgramRef())
		}
		a := b.action
		actions = append(actions, actionFact{a.Name(), a.Description(), a.Preconditions(), a.Effects(), b.policy, child, b.child.Budget, b.child.Capabilities})
	}
	configuration := struct {
		Goal, Description string
		Conditions        []Condition
		Actions           []actionFact
		Attempts          run.Quota
	}{def.goal.Name(), def.goal.Description(), def.goal.Conditions(), actions, def.maxActionAttempts}
	return run.Bind[I, Output](compiler, run.BindingConfig{
		Definition: def, Dispatcher: dispatch, Revision: cfg.Revision,
		Configuration: configuration,
	})
}
