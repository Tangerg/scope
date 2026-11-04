package interaction

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/tool"
)

type Config struct {
	CompletionValidator CompletionValidator
	ContextPolicy       ContextPolicy
	ContextProcessor    ContextProcessor
	Name                string
	Description         string
	Revision            string
	Model               ModelSource
	Tools               ToolSet
	// MaxModelCalls is unlimited by default; a finite zero cannot call a model.
	MaxModelCalls            run.Quota
	RequiredToolCapabilities run.CapabilitySet
	MaxConcurrentToolCalls   int
	MaxResponseBytes         int
	Delegates                []Delegate
}

// Compile freezes the real model loop, binds its child programs and checks its
// Go input/output against the same schemas used at dispatch and recovery.
func Compile(compiler *run.Compiler, cfg Config) (run.Program[Input, Output], error) {
	var zero run.Program[Input, Output]
	if cfg.Revision == "" {
		return zero, fmt.Errorf("%w: Revision must identify model and callback configuration", run.ErrInvalidProgram)
	}
	definition, err := newDefinition(definitionConfig{
		CompletionValidator:      cfg.CompletionValidator,
		MaxResponseBytes:         cfg.MaxResponseBytes,
		ContextPolicy:            cfg.ContextPolicy,
		PrepareContext:           cfg.ContextProcessor != nil,
		Name:                     cfg.Name,
		Description:              cfg.Description,
		MaxModelCalls:            cfg.MaxModelCalls,
		Tools:                    cfg.Tools,
		RequiredToolCapabilities: cfg.RequiredToolCapabilities,
		MaxConcurrentToolCalls:   cfg.MaxConcurrentToolCalls,
		Delegates:                cfg.Delegates,
	})
	if err != nil {
		return zero, err
	}
	dispatcher, err := newDispatcher(definition, dispatcherConfig{
		Tools:                    cfg.Tools,
		RequiredToolCapabilities: cfg.RequiredToolCapabilities,
		ContextProcessor:         cfg.ContextProcessor,
		Model:                    cfg.Model,
	})
	if err != nil {
		return zero, err
	}
	type delegateFact struct {
		Contract     tool.Definition
		Ref          run.ProgramRef
		Budget       run.Budget
		Capabilities run.CapabilitySet
	}
	var delegates []delegateFact
	for _, d := range definition.delegates {
		delegates = append(delegates, delegateFact{d.definition, d.child.Program.ProgramRef(), d.child.Budget, d.child.Capabilities})
	}
	policy := struct {
		LocalContext, ExternalContext, ValidateCompletion             bool
		MaxModelCalls                                                 run.Quota
		Tools                                                         toolConfiguration
		RequiredToolCapabilities                                      run.CapabilitySet
		MaxConcurrentToolCalls, MaxResponseBytes, MaxStreamDeltaBytes int
		Delegates                                                     []delegateFact
	}{
		LocalContext:             cfg.ContextPolicy != nil,
		ExternalContext:          cfg.ContextProcessor != nil,
		ValidateCompletion:       cfg.CompletionValidator != nil,
		MaxModelCalls:            definition.maxModelCalls,
		Tools:                    cfg.Tools.configuration(),
		RequiredToolCapabilities: dispatcher.requiredToolCapabilities,
		MaxConcurrentToolCalls:   definition.maxConcurrentToolCalls,
		MaxResponseBytes:         definition.maxResponseBytes,
		MaxStreamDeltaBytes:      dispatcher.model.streamLimit(),
		Delegates:                delegates,
	}
	return run.Bind[Input, Output](compiler, run.BindingConfig{
		Definition:    definition,
		Dispatcher:    dispatcher,
		Revision:      cfg.Revision,
		Configuration: policy,
	})
}
