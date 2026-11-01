package planning

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// Config binds a planner, its world sensor and executable actions.
// CallbackIdentity identifies their opaque settings, including cost functions
// and child input conversion. The compiler hashes the declared planning domain.
type Config struct {
	Name            string
	Description     string
	Goal            Goal
	Actions         []ActionBinding
	Planner         Planner
	Sensor          Sensor
	ActionExecutors map[string]ActionExecutor
	// MaxActionAttempts is unlimited by default; a finite zero cannot admit an action.
	MaxActionAttempts run.Quota
	BuildIdentity     run.Digest
	CallbackIdentity  run.Digest
}

func Compile[I any](ctx context.Context, cfg Config) (run.Program[I, Output], error) {
	var zero run.Program[I, Output]
	if err := run.RequireContext(ctx).Err(); err != nil {
		return zero, err
	}
	if !cfg.BuildIdentity.Valid() || !cfg.CallbackIdentity.Valid() {
		return zero, fmt.Errorf("%w: BuildIdentity and CallbackIdentity are required", run.ErrInvalidProgram)
	}
	in, err := jsonschema.For[I]()
	if err != nil {
		return zero, err
	}
	def, err := NewDefinition(DefinitionConfig{Name: cfg.Name, Description: cfg.Description, InputSchema: in, Goal: cfg.Goal, Actions: cfg.Actions, Planner: cfg.Planner, MaxActionAttempts: cfg.MaxActionAttempts})
	if err != nil {
		return zero, err
	}
	dispatch, err := NewDispatcher(def, DispatcherConfig{Sensor: cfg.Sensor, ActionExecutors: cfg.ActionExecutors})
	if err != nil {
		return zero, err
	}
	type actionFact struct {
		Name, Description      string
		Preconditions, Effects []Condition
		Required               run.CapabilitySet
		Child                  string
		Budget                 run.Budget
		Capabilities           run.CapabilitySet
	}
	actions := make([]actionFact, 0, len(def.bindings))
	for _, b := range def.bindings {
		var child string
		if b.delegatesToChild() {
			child = b.childDeployment.ProgramRef().Digest().String()
		}
		a := b.action
		actions = append(actions, actionFact{a.Name(), a.Description(), a.Preconditions(), a.Effects(), b.required, child, b.childBudget, b.childCapabilities})
	}
	wire, err := jsonv2.Marshal(struct {
		Goal, Description string
		Conditions        []Condition
		Actions           []actionFact
		Attempts          run.Quota
		Callbacks         run.Digest
	}{def.goal.Name(), def.goal.Description(), def.goal.Conditions(), actions, def.maxActionAttempts, cfg.CallbackIdentity}, jsonv2.Deterministic(true))
	if err != nil {
		return zero, err
	}
	return run.Bind[I, Output](run.BindingConfig{Definition: def, Dispatcher: dispatch, ImplementationDigest: cfg.BuildIdentity, ConfigurationDigest: run.ComputeDigest(wire)})
}
