package workflow

import (
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/run"
)

// Config declares one Workflow. Stages execute in declaration order and must
// have exactly matching adjacent schemas. Revision identifies callbacks that
// cannot be inspected. Configuration records their captured settings;
// structural stage settings belong to the graph.
type Config struct {
	Name          string
	Description   string
	Stages        []Stage
	Revision      string
	Configuration any
}

func Compile[I, O any](compiler *run.Compiler, cfg Config) (run.Program[I, O], error) {
	var zero run.Program[I, O]

	def, err := newDefinition(cfg)
	if err != nil {
		return zero, err
	}

	callbacks := slices.ContainsFunc(def.stages, Stage.carriesCallbacks)
	if callbacks != (cfg.Revision != "") || !callbacks && cfg.Configuration != nil {
		return zero, fmt.Errorf("%w: Revision must identify the configured stage callbacks", run.ErrInvalidProgram)
	}
	configuration := struct {
		Graph     Topology `json:"graph"`
		Callbacks any      `json:"callbacks,omitzero"`
	}{Graph: def.Topology(), Callbacks: cfg.Configuration}
	return run.Bind[I, O](compiler, run.BindingConfig{
		Definition: def, Revision: cfg.Revision,
		Configuration: configuration,
	})
}
