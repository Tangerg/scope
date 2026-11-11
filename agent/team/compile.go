package team

import (
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// Config grants the coordinator and workers their exact programs and resource
// authority. Input and output schemas come from Compile's type parameters.
// The coordinator and worker bindings enter the Program identity through
// ChildPrograms; every other field enters its configuration digest.
type Config struct {
	Name        string
	Description string
	// Coordinator must accept Turn and return Decision with exactly their
	// core/jsonschema.For contracts. It may itself be any Strategy.
	Coordinator run.ChildBinding
	Workers     []run.ChildBinding
	// MaxTurns is unlimited by default; a finite zero cannot admit the first turn.
	MaxTurns run.Quota
	// MaxTasks counts every task attempt, including rejected starts. It is
	// unlimited by default; a finite zero forbids worker starts.
	MaxTasks run.Quota
	// MaxConcurrentTasks counts admitted tasks until their drained outcomes
	// have been observed.
	MaxConcurrentTasks uint32
	// MaxControlsPerTurn bounds each Decision's controls independently of
	// cumulative work.
	MaxControlsPerTurn uint32
}

func Compile[I, O any](compiler *run.Compiler, cfg Config) (run.Program[I, O], error) {
	var zero run.Program[I, O]

	in, err := jsonschema.For[I]()
	if err != nil {
		return zero, err
	}
	out, err := jsonschema.For[O]()
	if err != nil {
		return zero, err
	}
	def, err := newDefinition(cfg, in, out)
	if err != nil {
		return zero, err
	}
	type grant struct {
		Ref          run.ProgramRef
		Budget       run.Budget
		Capabilities run.CapabilitySet
	}
	binding := func(b run.ChildBinding) grant { return grant{b.Program.ProgramRef(), b.Budget, b.Capabilities} }
	workers := make([]grant, 0, len(def.workers))
	for _, b := range def.workers {
		workers = append(workers, binding(b))
	}
	configuration := struct {
		Coordinator          grant
		Workers              []grant
		Turns, Tasks         run.Quota
		Concurrent, Controls uint32
	}{binding(def.coordinator), workers, def.maxTurns, def.maxTasks, def.maxConcurrentTasks, def.maxControlsPerTurn}
	return run.Bind[I, O](compiler, run.BindingConfig{
		Definition:    def,
		Configuration: configuration,
	})
}
