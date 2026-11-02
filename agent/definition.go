package agent

import (
	"context"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/tool"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
)

const executionStateKind = "interaction"

// DefinitionConfig describes immutable Interaction behavior. Cumulative quotas
// default to unlimited; the Host retains cancellation and capacity controls.
type DefinitionConfig struct {
	// MaxResponseBytes bounds each prepared-context and model-result settlement. Zero uses the runtime payload limit.
	MaxResponseBytes int
	ContextPolicy    ContextPolicy
	PrepareContext   bool
	Name             string

	Description string

	// MaxModelCalls bounds model Effects in one Interaction. Its zero value is
	// unlimited; a finite zero is rejected because an Interaction needs a model call.
	MaxModelCalls run.Quota

	// Tools is the frozen ordinary Tool authority. The Interaction owns its
	// Program as one of its ChildPrograms.
	Tools ToolSet

	// ToolBudget is allocated from the parent for each ordinary Tool child.
	// Its quotas also apply to input continuations; zero quotas are unlimited.
	ToolBudget run.Budget

	// ToolCapabilities is the attenuated authority granted to each Tool child.
	ToolCapabilities run.CapabilitySet

	// MaxConcurrentToolCalls bounds active calls declared safe to overlap.
	// Zero means one; calls without a concurrency declaration execute alone.
	MaxConcurrentToolCalls int

	// Delegates is the frozen model-visible manifest of exact child
	// Programs. Names must be unique within this slice and must not collide
	// with ordinary Tools in ToolSet.
	Delegates []Delegate

	// CompletionValidator optionally verifies a proposed final semantic output
	// against the current WorkingContext and accumulated typed Delegate
	// Artifacts. It is a pure Strategy callback whose identity must be covered by
	// the Program's ConfigurationDigest. Nil accepts every otherwise valid
	// completion.
	CompletionValidator CompletionValidator
}

// Definition is an immutable managed model/Tool-loop definition. It never
// calls a model client or an executable Tool: the model Dispatcher owns model
// I/O, and the ToolSet's child Program, which the Definition holds only as
// its binding, owns Tool execution.
type Definition struct {
	maxResponseBytes       int
	contextPolicy          ContextPolicy
	prepareContext         bool
	descriptor             run.Descriptor
	maxModelCalls          run.Quota
	delegates              []Delegate
	delegatesByName        map[string]int
	completionValidator    CompletionValidator
	tools                  toolManifest
	toolDeployment         run.Program[run.Payload, run.Payload]
	toolBudget             run.Budget
	toolCapabilities       run.CapabilitySet
	maxConcurrentToolCalls int
}

func NewDefinition(config DefinitionConfig) (*Definition, error) {
	if config.MaxResponseBytes < 0 || config.MaxResponseBytes > run.MaxPayloadBytes {
		return nil, fmt.Errorf("%w: invalid MaxResponseBytes", ErrInvalidDefinitionConfig)
	}
	limit := config.MaxResponseBytes
	if limit == 0 {
		limit = run.MaxPayloadBytes
	}
	if !config.MaxModelCalls.Allows(1) {
		return nil, fmt.Errorf("%w: MaxModelCalls must admit one model call", ErrInvalidDefinitionConfig)
	}
	if config.MaxConcurrentToolCalls < 0 {
		return nil, fmt.Errorf("%w: invalid Tool scheduling policy", ErrInvalidDefinitionConfig)
	}
	if !config.Tools.Configured() && (config.ToolBudget != (run.Budget{}) ||
		len(config.ToolCapabilities.Values()) != 0 || config.MaxConcurrentToolCalls != 0) {
		return nil, fmt.Errorf("%w: Tool policy requires a valid ToolSet", ErrInvalidDefinitionConfig)
	}
	descriptor, err := newDescriptor(config.Name, config.Description)
	if err != nil {
		return nil, err
	}
	delegates := slices.Clone(config.Delegates)
	names, err := indexDelegates(delegates, config.Tools.manifest())
	if err != nil {
		return nil, err
	}
	return &Definition{maxResponseBytes: limit, contextPolicy: config.ContextPolicy, prepareContext: config.PrepareContext,
		descriptor:             descriptor,
		maxModelCalls:          config.MaxModelCalls,
		delegates:              delegates,
		delegatesByName:        names,
		completionValidator:    config.CompletionValidator,
		tools:                  config.Tools.manifest(),
		toolDeployment:         config.Tools.deployment,
		toolBudget:             config.ToolBudget,
		toolCapabilities:       config.ToolCapabilities,
		maxConcurrentToolCalls: max(1, config.MaxConcurrentToolCalls),
	}, nil
}

func newDescriptor(name, description string) (run.Descriptor, error) {
	inputSchema, err := jsonschema.For[Input]()
	if err != nil {
		return run.Descriptor{}, fmt.Errorf("%w: input schema: %w", ErrInvalidDefinitionConfig, err)
	}
	outputSchema, err := jsonschema.For[Output]()
	if err != nil {
		return run.Descriptor{}, fmt.Errorf("%w: output schema: %w", ErrInvalidDefinitionConfig, err)
	}
	signalSchema, err := jsonschema.For[steerSignal]()
	if err != nil {
		return run.Descriptor{}, fmt.Errorf("%w: signal schema: %w", ErrInvalidDefinitionConfig, err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name:         name,
		Description:  description,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		SignalSchema: signalSchema,
	})
	if err != nil {
		return run.Descriptor{}, fmt.Errorf("%w: descriptor: %w", ErrInvalidDefinitionConfig, err)
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

// toolWindow sizes the Tool batch that starts at the first remaining call:
// the longest concurrent run the ToolSet admits, or one call when Tool calls
// never overlap. Step sizes a batch with it and Restore requires it.
func (d *Definition) toolWindow(ctx context.Context, remaining []chat.ToolCall) (int, error) {
	if d.maxConcurrentToolCalls == 1 {
		return 1, nil
	}
	return d.tools.concurrentBatchEnd(ctx, remaining)
}

// ChildPrograms reports the Tool child binding, when Tools are configured,
// and every Delegate binding.
func (d *Definition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	if d == nil {
		return nil
	}
	var children []run.Program[run.Payload, run.Payload]
	if d.toolDeployment.Valid() {
		children = append(children, d.toolDeployment)
	}
	for _, delegate := range d.delegates {
		children = append(children, delegate.deployment)
	}
	return children
}

func (d *Definition) Descriptor() run.Descriptor {
	if d == nil {
		return run.Descriptor{}
	}
	return d.descriptor
}

func (d *Definition) Start(input run.Payload) (run.Execution, error) {
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
	return &execution{
		definition: d,
		state:      executionState{WorkingContext: decoded.clone()},
	}, nil
}

// Restore recreates an Interaction solely from its opaque state. It accepts
// the current Interaction state schema and rejects unknown fields.
func (d *Definition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if !d.valid() {
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

var _ run.Definition = (*Definition)(nil)

func (d *Definition) modelDefinitions(advertisedToolNames []string) ([]tool.Definition, error) {
	tools := d.tools
	if err := tools.validateAdvertisements(advertisedToolNames); err != nil {
		return nil, fmt.Errorf("interaction: advertised Tools: %w", err)
	}
	definitions := cloneDefinitions(tools.initialDefinitions)
	for _, delegate := range d.delegates {
		definitions = append(definitions, delegate.definition.Clone())
	}
	for _, name := range advertisedToolNames {
		definitions = append(definitions, tools.entries[name].contract.Definition())
	}
	return definitions, nil
}

func (d *Definition) settlePreparedContext(req run.EffectRequest, messages []chat.Message) (run.Settlement, error) {
	if !d.valid() || !d.prepareContext {
		return run.Settlement{}, ErrInvalidDispatcherConfig
	}
	if !req.Valid() {
		return run.Settlement{}, ErrInvalidProtocol
	}
	env, err := decodeEffect(req.Effect().Payload())
	if err != nil {
		return run.Settlement{}, err
	}
	if env.operation() != operationPrepare {
		return run.Settlement{}, ErrInvalidProtocol
	}
	effective := env.Preparation.Request.request()
	effective.Messages = cloneMessages(messages)
	effective.Tools, err = d.modelDefinitions(env.Preparation.AdvertisedToolNames)
	if err != nil {
		return run.Settlement{}, err
	}
	if err = effective.Validate(); err != nil {
		return run.Settlement{}, err
	}
	payload, err := run.EncodePayload(signalEnvelope{Prepared: &preparedContext{Messages: effective.Messages}})
	if err != nil {
		return run.Settlement{}, err
	}
	if len(payload.JSON()) > d.maxResponseBytes {
		return run.Settlement{}, ErrModelResponseTooLarge
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload.JSON())
}
