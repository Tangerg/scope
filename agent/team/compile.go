package team

import (
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// Config grants the coordinator and workers their exact programs and resource
// authority. Input and output schemas come from Compile's type parameters.
type Config struct {
	Name        string
	Description string
	Coordinator agentrun.ChildBinding
	Workers     []agentrun.ChildBinding
	// MaxTurns is unlimited by default; a finite zero cannot admit the first turn.
	MaxTurns agentrun.Quota
	// MaxTasks is unlimited by default; finite zero forbids worker startups.
	MaxTasks           agentrun.Quota
	MaxConcurrentTasks uint32
	MaxControlsPerTurn uint32
}

func Compile[I, O any](compiler *agentrun.Compiler, cfg Config) (agentrun.Program[I, O], error) {
	var zero agentrun.Program[I, O]

	in, err := jsonschema.For[I]()
	if err != nil {
		return zero, err
	}
	out, err := jsonschema.For[O]()
	if err != nil {
		return zero, err
	}
	def, err := newDefinition(definitionConfig{Name: cfg.Name, Description: cfg.Description, StateSchema: in, OutputSchema: out, Coordinator: cfg.Coordinator, Workers: cfg.Workers, MaxTurns: cfg.MaxTurns, MaxTasks: cfg.MaxTasks, MaxConcurrentTasks: cfg.MaxConcurrentTasks, MaxControlsPerTurn: cfg.MaxControlsPerTurn})
	if err != nil {
		return zero, err
	}
	type grant struct {
		Ref          agentrun.ProgramRef
		Budget       agentrun.Budget
		Capabilities agentrun.CapabilitySet
	}
	binding := func(b agentrun.ChildBinding) grant { return grant{b.Program.ProgramRef(), b.Budget, b.Capabilities} }
	workers := make([]grant, 0, len(def.workers))
	for _, b := range def.workers {
		workers = append(workers, binding(b))
	}
	configuration := struct {
		Coordinator          grant
		Workers              []grant
		Turns, Tasks         agentrun.Quota
		Concurrent, Controls uint32
	}{binding(def.coordinator), workers, def.maxTurns, def.maxTasks, def.maxConcurrentTasks, def.maxControlsPerTurn}
	return agentrun.Bind[I, O](compiler, agentrun.BindingConfig{
		Definition:    def,
		Configuration: configuration,
	})
}
