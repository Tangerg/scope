package interaction

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/tool"
)

// Config describes one Interaction. Cumulative quotas default to unlimited;
// the Host retains cancellation and capacity controls.
type Config struct {
	// CompletionValidator optionally verifies a proposed final semantic output
	// against the current WorkingContext and accumulated typed Delegate
	// Artifacts. It is a pure Strategy callback whose identity must be covered
	// by Revision. Nil accepts every otherwise valid completion.
	CompletionValidator CompletionValidator
	ContextPolicy       ContextPolicy
	// ContextProcessor, when set, prepares every model context as an external
	// Effect before the model call.
	ContextProcessor ContextProcessor
	Name             string
	Description      string
	Revision         string
	Model            ModelSource
	// Tools freezes tool admission and execution policy.
	Tools ToolSet
	// MaxModelCalls bounds model Effects in one Interaction. Its zero value is
	// unlimited; a finite zero is rejected because an Interaction needs a model call.
	MaxModelCalls            run.Quota
	RequiredToolCapabilities run.CapabilitySet
	// MaxConcurrentToolCalls bounds active calls declared safe to overlap.
	// Zero means one; calls without a concurrency declaration execute alone.
	MaxConcurrentToolCalls int
	// MaxResponseBytes bounds each prepared-context and model-result
	// settlement. Zero uses the runtime payload limit; a limit below the
	// smallest complete model response is rejected.
	MaxResponseBytes int
	// Delegates is the frozen model-visible manifest of exact child Programs.
	// Names must be unique and must not collide with ordinary Tools.
	Delegates []Delegate
}

// Compile freezes the real model loop, binds its child programs and checks its
// Go input/output against the same schemas used at dispatch and recovery.
func Compile(compiler *run.Compiler, cfg Config) (run.Program[Input, Output], error) {
	var zero run.Program[Input, Output]
	if cfg.Revision == "" {
		return zero, fmt.Errorf("%w: Revision must identify model and callback configuration", run.ErrInvalidProgram)
	}
	definition, err := newDefinition(cfg)
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
