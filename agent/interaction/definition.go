package interaction

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

const executionStateKind = "interaction"

// DefinitionConfig describes immutable Interaction behavior. MaxModelCalls is
// required because a model-directed loop must have an explicit local stop
// condition in addition to Engine-wide Effect and Step limits.
type DefinitionConfig struct {
	// Name is the stable qualified Definition name.
	Name string

	// Description states the managed behavior for discovery.
	Description string

	// MaxModelCalls bounds model Effects in one Interaction. It must be positive.
	MaxModelCalls uint32

	// Tools is the frozen ordinary Tool authority. Its Deployment must be
	// available through Engine's exact DeploymentResolver.
	Tools ToolSet

	// ToolBudget is allocated from the parent for each ordinary Tool child.
	// It is required when Tools is present and also bounds input continuations.
	ToolBudget agent.Budget

	// ToolCapabilities is the attenuated authority granted to each Tool child.
	ToolCapabilities agent.CapabilitySet

	// MaxConcurrentToolCalls bounds active calls declared safe to overlap.
	// Zero means one; calls without a concurrency declaration execute alone.
	MaxConcurrentToolCalls int

	// Delegates is the frozen model-visible manifest of exact child
	// Deployments. Names must be unique within this slice and must not collide
	// with ordinary Tools in ToolSet.
	Delegates []Delegate

	// CompletionValidator optionally verifies a proposed final semantic output
	// against the current WorkingContext and accumulated typed Delegate
	// Artifacts. It is a pure Strategy callback whose identity must be covered by
	// the Deployment's ConfigurationDigest. Nil accepts every otherwise valid
	// completion.
	CompletionValidator CompletionValidator
}

// Definition is an immutable managed model/Tool-loop definition. It contains
// no model client or executable Tool; those external capabilities belong to
// the model Dispatcher and ToolSet child Deployment.
type Definition struct {
	descriptor             agent.Descriptor
	maxModelCalls          uint32
	delegates              []Delegate
	completionValidator    CompletionValidator
	tools                  toolManifest
	toolBudget             agent.Budget
	toolCapabilities       agent.CapabilitySet
	maxConcurrentToolCalls int
}

// NewDefinition freezes the managed contract, delegates, completion policy,
// and model-call limit for one interaction loop. The provider client and
// executable Tools remain bound to their external dispatch boundaries.
func NewDefinition(config DefinitionConfig) (*Definition, error) {
	if config.MaxModelCalls == 0 {
		return nil, fmt.Errorf("%w: MaxModelCalls must be positive", ErrInvalidDefinitionConfig)
	}
	if config.MaxConcurrentToolCalls < 0 || !config.ToolCapabilities.Valid() {
		return nil, fmt.Errorf("%w: invalid Tool scheduling policy", ErrInvalidDefinitionConfig)
	}
	if config.Tools.Valid() && !config.ToolBudget.Valid() {
		return nil, fmt.Errorf("%w: ToolBudget is required with Tools", ErrInvalidDefinitionConfig)
	}
	if !config.Tools.Valid() && (config.Tools.dispatcher != nil || config.ToolBudget != (agent.Budget{}) ||
		len(config.ToolCapabilities.Values()) != 0 || config.MaxConcurrentToolCalls != 0) {
		return nil, fmt.Errorf("%w: Tool policy requires a valid ToolSet", ErrInvalidDefinitionConfig)
	}
	inputSchema, err := agent.SchemaFor[Input]()
	if err != nil {
		return nil, fmt.Errorf("%w: input schema: %w", ErrInvalidDefinitionConfig, err)
	}
	outputSchema, err := agent.SchemaFor[Output]()
	if err != nil {
		return nil, fmt.Errorf("%w: output schema: %w", ErrInvalidDefinitionConfig, err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name:         config.Name,
		Description:  config.Description,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: descriptor: %w", ErrInvalidDefinitionConfig, err)
	}
	delegates := slices.Clone(config.Delegates)
	names := make(map[string]struct{}, len(delegates))
	for index, delegate := range delegates {
		if !delegate.Valid() {
			return nil, fmt.Errorf("%w: Delegates[%d]: %w", ErrInvalidDefinitionConfig, index, ErrInvalidDelegate)
		}
		name := delegate.definition.Name
		if _, duplicate := config.Tools.manifest.entries[name]; duplicate {
			return nil, fmt.Errorf("%w: Delegate name %q collides with a Tool", ErrInvalidDefinitionConfig, name)
		}
		if _, duplicate := names[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Delegate name %q", ErrInvalidDefinitionConfig, name)
		}
		names[name] = struct{}{}
	}
	return &Definition{
		descriptor: descriptor, maxModelCalls: config.MaxModelCalls,
		delegates:           delegates,
		completionValidator: config.CompletionValidator,
		tools:               config.Tools.manifest, toolBudget: config.ToolBudget,
		toolCapabilities:       config.ToolCapabilities,
		maxConcurrentToolCalls: max(1, config.MaxConcurrentToolCalls),
	}, nil
}

// Descriptor returns the immutable model-visible Definition contract.
func (d *Definition) Descriptor() agent.Descriptor {
	if d == nil {
		return agent.Descriptor{}
	}
	return d.descriptor
}

// Start creates a fresh Interaction from validated caller input.
func (d *Definition) Start(input agent.Input) (agent.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidDefinitionConfig
	}
	decoded, err := input.Decode[Input]()
	if err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrInvalidInput, err)
	}
	if err := decoded.Validate(); err != nil {
		return nil, err
	}
	request := &chat.Request{
		Messages: cloneMessages(decoded.Messages),
		Options:  decoded.Options.Clone(),
	}
	return &execution{
		definition: d,
		state: executionState{
			Phase:          phaseReadyModel,
			WorkingContext: request,
		},
	}, nil
}

// Restore recreates an Interaction solely from its opaque state.
func (d *Definition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidDefinitionConfig
	}
	if state.Kind() != executionStateKind {
		return nil, fmt.Errorf("%w: unsupported kind", ErrInvalidExecutionState)
	}
	var decoded executionState
	if err := jsonv2.Unmarshal(state.Payload(), &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrInvalidExecutionState, err)
	}
	if err := decoded.Validate(d); err != nil {
		return nil, err
	}
	return &execution{definition: d, state: decoded}, nil
}

func (d *Definition) valid() bool {
	return d != nil && d.descriptor.Valid()
}

func (d *Definition) delegate(name string) (Delegate, bool) {
	if d == nil {
		return Delegate{}, false
	}
	for _, delegate := range d.delegates {
		if delegate.definition.Name == name {
			return delegate, true
		}
	}
	return Delegate{}, false
}

func encodeState(state executionState) (agent.ExecutionState, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return agent.ExecutionState{}, fmt.Errorf("interaction: encode execution state: %w", err)
	}
	return agent.NewExecutionState(executionStateKind, payload)
}
