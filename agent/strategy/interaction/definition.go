package interaction

import (
	"context"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/restore"
	"github.com/Tangerg/scope/core/chat"
)

const executionStateKind = "interaction"

// DefinitionConfig describes immutable Interaction behavior. Cumulative quotas
// default to unlimited; the Host retains cancellation and capacity controls.
type DefinitionConfig struct {
	Name string

	Description string

	// MaxModelCalls bounds model Effects in one Interaction. Its zero value is
	// unlimited; a finite zero is rejected because an Interaction needs a model call.
	MaxModelCalls agent.Quota

	// Tools is the frozen ordinary Tool authority. The Interaction owns its
	// Deployment as one of its ChildDeployments.
	Tools ToolSet

	// ToolBudget is allocated from the parent for each ordinary Tool child.
	// Its quotas also apply to input continuations; zero quotas are unlimited.
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

// Definition is an immutable managed model/Tool-loop definition. It never
// calls a model client or an executable Tool: the model Dispatcher owns model
// I/O, and the ToolSet's child Deployment, which the Definition holds only as
// its binding, owns Tool execution.
type Definition struct {
	descriptor             agent.Descriptor
	maxModelCalls          agent.Quota
	delegates              []Delegate
	delegatesByName        map[string]int
	completionValidator    CompletionValidator
	tools                  toolManifest
	toolDeployment         agent.Deployment
	toolBudget             agent.Budget
	toolCapabilities       agent.CapabilitySet
	maxConcurrentToolCalls int
}

func NewDefinition(config DefinitionConfig) (*Definition, error) {
	if !config.MaxModelCalls.Allows(1) {
		return nil, fmt.Errorf("%w: MaxModelCalls must admit one model call", ErrInvalidDefinitionConfig)
	}
	if config.MaxConcurrentToolCalls < 0 || !config.ToolCapabilities.Valid() {
		return nil, fmt.Errorf("%w: invalid Tool scheduling policy", ErrInvalidDefinitionConfig)
	}
	if !config.Tools.Configured() && (config.ToolBudget != (agent.Budget{}) ||
		len(config.ToolCapabilities.Values()) != 0 || config.MaxConcurrentToolCalls != 0) {
		return nil, fmt.Errorf("%w: Tool policy requires a valid ToolSet", ErrInvalidDefinitionConfig)
	}
	descriptor, err := newDescriptor(config.Name, config.Description)
	if err != nil {
		return nil, err
	}
	delegates := slices.Clone(config.Delegates)
	names, err := indexDelegates(delegates, config.Tools.manifest)
	if err != nil {
		return nil, err
	}
	return &Definition{
		descriptor:             descriptor,
		maxModelCalls:          config.MaxModelCalls,
		delegates:              delegates,
		delegatesByName:        names,
		completionValidator:    config.CompletionValidator,
		tools:                  config.Tools.manifest,
		toolDeployment:         config.Tools.deployment,
		toolBudget:             config.ToolBudget,
		toolCapabilities:       config.ToolCapabilities,
		maxConcurrentToolCalls: max(1, config.MaxConcurrentToolCalls),
	}, nil
}

func newDescriptor(name, description string) (agent.Descriptor, error) {
	inputSchema, err := agent.SchemaFor[Input]()
	if err != nil {
		return agent.Descriptor{}, fmt.Errorf("%w: input schema: %w", ErrInvalidDefinitionConfig, err)
	}
	outputSchema, err := agent.SchemaFor[Output]()
	if err != nil {
		return agent.Descriptor{}, fmt.Errorf("%w: output schema: %w", ErrInvalidDefinitionConfig, err)
	}
	signalSchema, err := agent.SchemaFor[steerSignal]()
	if err != nil {
		return agent.Descriptor{}, fmt.Errorf("%w: signal schema: %w", ErrInvalidDefinitionConfig, err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name:         name,
		Description:  description,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		SignalSchema: signalSchema,
	})
	if err != nil {
		return agent.Descriptor{}, fmt.Errorf("%w: descriptor: %w", ErrInvalidDefinitionConfig, err)
	}
	return descriptor, nil
}

func indexDelegates(delegates []Delegate, tools toolManifest) (map[string]int, error) {
	names := make(map[string]int, len(delegates))
	for index, delegate := range delegates {
		if !delegate.Valid() {
			return nil, fmt.Errorf("%w: Delegates[%d]: %w", ErrInvalidDefinitionConfig, index, ErrInvalidDelegate)
		}
		name := delegate.definition.Name
		if _, duplicate := tools.entries[name]; duplicate {
			return nil, fmt.Errorf("%w: Delegate name %q collides with a Tool", ErrInvalidDefinitionConfig, name)
		}
		if _, duplicate := names[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Delegate name %q", ErrInvalidDefinitionConfig, name)
		}
		names[name] = index
	}
	return names, nil
}

// ChildDeployments reports the Tool child binding, when Tools are configured,
// and every Delegate binding.
func (d *Definition) ChildDeployments() []agent.Deployment {
	if d == nil {
		return nil
	}
	var children []agent.Deployment
	if d.toolDeployment.Valid() {
		children = append(children, d.toolDeployment)
	}
	for _, delegate := range d.delegates {
		children = append(children, delegate.deployment)
	}
	return children
}

func (d *Definition) Descriptor() agent.Descriptor {
	if d == nil {
		return agent.Descriptor{}
	}
	return d.descriptor
}

func (d *Definition) Start(input agent.Payload) (agent.Execution, error) {
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

// Restore recreates an Interaction solely from its opaque state. It accepts
// the current Interaction state schema and rejects unknown fields.
func (d *Definition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	if !(d.valid()) {
		return nil, ErrInvalidDefinitionConfig
	}
	decoded, err := restore.Decode(ctx, state, executionStateKind, ErrInvalidExecutionState, func(ctx context.Context, decoded executionState) error {
		return decoded.validate(ctx, d)
	})
	if err != nil {
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
	index, found := d.delegatesByName[name]
	if !found {
		return Delegate{}, false
	}
	return d.delegates[index], true
}

var _ agent.Definition = (*Definition)(nil)
