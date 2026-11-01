package workflow

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
)

// Config binds the declared graph to executable code. CallbackIdentity covers
// captured settings of transforms, selectors and reducers that cannot be inferred.
type Config struct {
	Name             string
	Description      string
	Stages           []Stage
	BuildIdentity    run.Digest
	CallbackIdentity run.Digest
}

func Compile[I, O any](ctx context.Context, cfg Config) (run.Program[I, O], error) {
	var zero run.Program[I, O]
	if err := run.RequireContext(ctx).Err(); err != nil {
		return zero, err
	}
	def, err := NewDefinition(DefinitionConfig{Name: cfg.Name, Description: cfg.Description, Stages: cfg.Stages})
	if err != nil {
		return zero, err
	}
	if !cfg.BuildIdentity.Valid() {
		return zero, fmt.Errorf("%w: missing BuildIdentity", run.ErrInvalidProgram)
	}
	callbacks := false
	for _, stage := range def.stages {
		if stage.kind != StageKindCall {
			callbacks = true
			break
		}
	}
	if callbacks && !cfg.CallbackIdentity.Valid() {
		return zero, fmt.Errorf("%w: stages with callbacks require CallbackIdentity", run.ErrInvalidProgram)
	}
	wire, err := jsonv2.Marshal(struct {
		Topology  Topology
		Callbacks string
	}{def.Topology(), cfg.CallbackIdentity.String()}, jsonv2.Deterministic(true))
	if err != nil {
		return zero, err
	}
	return run.Bind[I, O](run.BindingConfig{Definition: def, ImplementationDigest: cfg.BuildIdentity, ConfigurationDigest: run.ComputeDigest(wire)})
}
