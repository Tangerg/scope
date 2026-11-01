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
// executable code; ModelIdentity and ToolIdentity identify opaque collaborator
// settings. Declared policies, manifests and child bindings are hashed here.
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
	Tools           []tool.Tool
	DeferredTools   []tool.Tool
	ToolIdentity    run.Digest
	// MaxModelCalls is unlimited by default; a finite zero cannot call a model.
	MaxModelCalls run.Quota
	// ToolBudget grants authority to each ordinary tool child.
	ToolBudget             run.Budget
	ToolCapabilities       run.CapabilitySet
	MaxConcurrentToolCalls int
	MaxResponseBytes       int
	Delegates              []Delegate
}

// Compile freezes the real model loop, binds its child programs and checks its
// Go input/output against the same schemas used at dispatch and recovery.
func Compile(ctx context.Context, cfg Config) (run.Program[Input, Output], error) {
	var zero run.Program[Input, Output]
	if err := run.RequireContext(ctx).Err(); err != nil {
		return zero, err
	}
	if !cfg.BuildIdentity.Valid() || !cfg.ModelIdentity.Valid() {
		return zero, fmt.Errorf("agent: BuildIdentity and ModelIdentity are required")
	}
	var tools ToolSet
	var err error
	if len(cfg.Tools)+len(cfg.DeferredTools) > 0 {
		if !cfg.ToolIdentity.Valid() {
			return zero, fmt.Errorf("agent: ToolIdentity is required for opaque tool configuration")
		}
		tools, err = NewToolSet(ToolSetConfig{Name: cfg.Name + ".tools", Description: "Execute the bound tools for " + cfg.Name + ".", Tools: cfg.Tools, DeferredTools: cfg.DeferredTools, ImplementationDigest: cfg.BuildIdentity, ConfigurationDigest: cfg.ToolIdentity})
		if err != nil {
			return zero, err
		}
	} else if cfg.ToolIdentity.Valid() {
		return zero, fmt.Errorf("agent: ToolIdentity requires tools")
	}
	if (cfg.ContextPolicy != nil || cfg.ContextProcessor != nil) != cfg.ContextIdentity.Valid() {
		return zero, fmt.Errorf("agent: ContextIdentity must identify the configured context callbacks")
	}
	definition, err := NewDefinition(DefinitionConfig{MaxResponseBytes: cfg.MaxResponseBytes, ContextPolicy: cfg.ContextPolicy, PrepareContext: cfg.ContextProcessor != nil, Name: cfg.Name, Description: cfg.Description, MaxModelCalls: cfg.MaxModelCalls, Tools: tools, ToolBudget: cfg.ToolBudget, ToolCapabilities: cfg.ToolCapabilities, MaxConcurrentToolCalls: cfg.MaxConcurrentToolCalls, Delegates: cfg.Delegates})
	if err != nil {
		return zero, err
	}
	dispatcher, err := NewDispatcher(definition, DispatcherConfig{ContextProcessor: cfg.ContextProcessor, Model: cfg.Model, Streamer: cfg.Streamer})
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
		ContextIdentity                          string
		LocalContext, ExternalContext            bool
		Model                                    run.Digest
		Streaming                                bool
		MaxModelCalls                            run.Quota
		ToolBudget                               run.Budget
		ToolCapabilities                         run.CapabilitySet
		MaxConcurrentToolCalls, MaxResponseBytes int
		Delegates                                []delegateFact
	}{cfg.ContextIdentity.String(), cfg.ContextPolicy != nil, cfg.ContextProcessor != nil, cfg.ModelIdentity, cfg.Streamer != nil, definition.maxModelCalls, definition.toolBudget, definition.toolCapabilities, definition.maxConcurrentToolCalls, definition.maxResponseBytes, delegates}
	encoded, err := jsonv2.Marshal(policy, jsonv2.Deterministic(true))
	if err != nil {
		return zero, fmt.Errorf("agent: freeze loop settings: %w", err)
	}
	return run.Bind[Input, Output](run.BindingConfig{Definition: definition, Dispatcher: dispatcher, ImplementationDigest: cfg.BuildIdentity, ConfigurationDigest: run.ComputeDigest(encoded)})
}
