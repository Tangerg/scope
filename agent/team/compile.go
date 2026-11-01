package team

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// Config grants the coordinator and workers their exact programs and resource
// authority. Input and output schemas come from Compile's type parameters.
type Config struct {
	Name        string
	Description string
	Coordinator WorkerConfig
	Workers     []WorkerConfig
	// MaxTurns is unlimited by default; a finite zero cannot admit the first turn.
	MaxTurns agentrun.Quota
	// MaxTasks is unlimited by default; finite zero forbids worker startups.
	MaxTasks           agentrun.Quota
	MaxConcurrentTasks uint32
	MaxControlsPerTurn uint32
	BuildIdentity      agentrun.Digest
}

func Compile[I, O any](ctx context.Context, cfg Config) (agentrun.Program[I, O], error) {
	var zero agentrun.Program[I, O]
	if err := agentrun.RequireContext(ctx).Err(); err != nil {
		return zero, err
	}
	if !cfg.BuildIdentity.Valid() {
		return zero, fmt.Errorf("%w: BuildIdentity is required", agentrun.ErrInvalidProgram)
	}
	in, err := jsonschema.For[I]()
	if err != nil {
		return zero, err
	}
	out, err := jsonschema.For[O]()
	if err != nil {
		return zero, err
	}
	def, err := NewDefinition(DefinitionConfig{Name: cfg.Name, Description: cfg.Description, StateSchema: in, OutputSchema: out, Coordinator: cfg.Coordinator, Workers: cfg.Workers, MaxTurns: cfg.MaxTurns, MaxTasks: cfg.MaxTasks, MaxConcurrentTasks: cfg.MaxConcurrentTasks, MaxControlsPerTurn: cfg.MaxControlsPerTurn})
	if err != nil {
		return zero, err
	}
	type grant struct {
		Ref          agentrun.ProgramRef
		Budget       agentrun.Budget
		Capabilities agentrun.CapabilitySet
	}
	binding := func(b childBinding) grant { return grant{b.deployment.ProgramRef(), b.budget, b.capabilities} }
	workers := make([]grant, 0, len(def.workers))
	for _, b := range def.workers {
		workers = append(workers, binding(b))
	}
	wire, err := jsonv2.Marshal(struct {
		Coordinator          grant
		Workers              []grant
		Turns, Tasks         agentrun.Quota
		Concurrent, Controls uint32
	}{binding(def.coordinator), workers, def.maxTurns, def.maxTasks, def.maxConcurrentTasks, def.maxControlsPerTurn}, jsonv2.Deterministic(true))
	if err != nil {
		return zero, err
	}
	return agentrun.Bind[I, O](agentrun.BindingConfig{Definition: def, ImplementationDigest: cfg.BuildIdentity, ConfigurationDigest: agentrun.ComputeDigest(wire)})
}
