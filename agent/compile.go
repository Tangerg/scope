package agent

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

// Config declares a model-and-tool loop. BuildIdentity identifies the exact
// executable code and ModelIdentity identifies the model configuration.
// A bound ToolSet owns its tool manifest, execution and recovery identity.
type Config struct {
	ContextPolicy    ContextPolicy
	ContextProcessor ContextProcessor
	// ContextIdentity identifies opaque callback configuration; no function is hashed.
	ContextIdentity run.Digest
	Name            string
	Description     string
	BuildIdentity   run.Digest
	ModelIdentity   run.Digest
	Model           chat.Model
	Streamer        chat.Streamer
	Tools           ToolSet
	// MaxModelCalls is unlimited by default; a finite zero cannot call a model.
	MaxModelCalls run.Quota
	// ToolBudget grants authority to each ordinary tool child.
	ToolBudget             run.Budget
	ToolCapabilities       run.CapabilitySet
	MaxConcurrentToolCalls int
	MaxResponseBytes       int
	// Streaming admission is independent of final settlement size; this uses
	// the same policy and default as DispatcherConfig.MaxStreamDeltaBytes.
	MaxStreamDeltaBytes int
	Delegates           []Delegate
}

// Compile freezes the real model loop, binds its child programs and checks its
// Go input/output against the same schemas used at dispatch and recovery.
func Compile(ctx context.Context, cfg Config) (run.Program[Input, Output], error) {
	var zero run.Program[Input, Output]
	if err := run.RequireContext(ctx).Err(); err != nil {
		return zero, err
	}
	if !cfg.BuildIdentity.Valid() || !cfg.ModelIdentity.Valid() {
		return zero, fmt.Errorf("%w: BuildIdentity and ModelIdentity are required", run.ErrInvalidProgram)
	}
	if (cfg.ContextPolicy != nil || cfg.ContextProcessor != nil) != cfg.ContextIdentity.Valid() {
		return zero, fmt.Errorf("%w: ContextIdentity must identify the configured context callbacks", run.ErrInvalidProgram)
	}
	definition, err := NewDefinition(DefinitionConfig{MaxResponseBytes: cfg.MaxResponseBytes, ContextPolicy: cfg.ContextPolicy, PrepareContext: cfg.ContextProcessor != nil, Name: cfg.Name, Description: cfg.Description, MaxModelCalls: cfg.MaxModelCalls, Tools: cfg.Tools, ToolBudget: cfg.ToolBudget, ToolCapabilities: cfg.ToolCapabilities, MaxConcurrentToolCalls: cfg.MaxConcurrentToolCalls, Delegates: cfg.Delegates})
	if err != nil {
		return zero, err
	}
	dispatcher, err := NewDispatcher(definition, DispatcherConfig{ContextProcessor: cfg.ContextProcessor, Model: cfg.Model, Streamer: cfg.Streamer, MaxStreamDeltaBytes: cfg.MaxStreamDeltaBytes})
	if err != nil {
		return zero, err
	}
	type delegateFact struct {
		Definition   tool.Definition
		Ref          run.ProgramRef
		Budget       run.Budget
		Capabilities run.CapabilitySet
	}
	var delegates []delegateFact
	for _, d := range definition.delegates {
		delegates = append(delegates, delegateFact{d.definition, d.deployment.ProgramRef(), d.budget, d.capabilities})
	}
	policy := struct {
		ContextIdentity                                               string
		LocalContext, ExternalContext                                 bool
		Model                                                         run.Digest
		Streaming                                                     bool
		MaxModelCalls                                                 run.Quota
		ToolBudget                                                    run.Budget
		ToolCapabilities                                              run.CapabilitySet
		MaxConcurrentToolCalls, MaxResponseBytes, MaxStreamDeltaBytes int
		Delegates                                                     []delegateFact
	}{
		ContextIdentity: cfg.ContextIdentity.String(), LocalContext: cfg.ContextPolicy != nil, ExternalContext: cfg.ContextProcessor != nil,
		Model: cfg.ModelIdentity, Streaming: cfg.Streamer != nil, MaxModelCalls: definition.maxModelCalls,
		ToolBudget: definition.toolBudget, ToolCapabilities: definition.toolCapabilities, MaxConcurrentToolCalls: definition.maxConcurrentToolCalls,
		MaxResponseBytes: definition.maxResponseBytes, MaxStreamDeltaBytes: dispatcher.maxStreamDeltaBytes, Delegates: delegates,
	}
	encoded, err := jsonv2.Marshal(policy, jsonv2.Deterministic(true))
	if err != nil {
		return zero, fmt.Errorf("agent: freeze loop settings: %w", err)
	}
	return run.Bind[Input, Output](run.BindingConfig{Definition: definition, Dispatcher: dispatcher, ImplementationDigest: cfg.BuildIdentity, ConfigurationDigest: run.ComputeDigest(encoded)})
}
