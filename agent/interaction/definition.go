package interaction

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/tool"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
)

const executionStateKind = "interaction"

// The definition retains immutable declarations and pure validators. Executable
// models and tools belong exclusively to the dispatcher.
type definition struct {
	maxResponseBytes       int
	contextPolicy          ContextPolicy
	prepareContext         bool
	descriptor             run.Descriptor
	maxModelCalls          run.Quota
	delegates              []Delegate
	delegatesByName        map[string]int
	completionValidator    CompletionValidator
	tools                  toolManifest
	maxConcurrentToolCalls int
}

func newDefinition(config Config) (*definition, error) {
	limit, err := responseLimit(config.MaxResponseBytes)
	if err != nil {
		return nil, err
	}
	if !config.MaxModelCalls.Allows(1) {
		return nil, fmt.Errorf("%w: MaxModelCalls must admit one model call", ErrInvalidConfig)
	}
	if config.MaxConcurrentToolCalls < 0 {
		return nil, fmt.Errorf("%w: invalid Tool scheduling policy", ErrInvalidConfig)
	}
	if !config.Tools.Configured() && (len(config.RequiredToolCapabilities.Values()) != 0 || config.MaxConcurrentToolCalls != 0) {
		return nil, fmt.Errorf("%w: Tool policy requires a valid ToolSet", ErrInvalidConfig)
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
	return &definition{maxResponseBytes: limit, contextPolicy: config.ContextPolicy, prepareContext: config.ContextProcessor != nil,
		descriptor:             descriptor,
		maxModelCalls:          config.MaxModelCalls,
		delegates:              delegates,
		delegatesByName:        names,
		completionValidator:    config.CompletionValidator,
		tools:                  config.Tools.manifest(),
		maxConcurrentToolCalls: max(1, config.MaxConcurrentToolCalls),
	}, nil
}

// responseLimit resolves MaxResponseBytes once, at construction: a limit no
// complete model response fits would otherwise fail every model call alike.
func responseLimit(configured int) (int, error) {
	if configured < 0 || configured > run.MaxPayloadBytes {
		return 0, fmt.Errorf("%w: invalid MaxResponseBytes", ErrInvalidConfig)
	}
	if configured == 0 {
		return run.MaxPayloadBytes, nil
	}
	// A content-free stop is the smallest complete chat response, measured with
	// the same owner and representation as every model settlement.
	minimum, err := (modelCallResult{Response: &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}}}).settlement()
	if err != nil {
		return 0, err
	}
	if len(minimum.Payload().JSON()) > configured {
		return 0, fmt.Errorf("%w: MaxResponseBytes %d cannot hold the smallest model response", ErrInvalidConfig, configured)
	}
	return configured, nil
}

func newDescriptor(name, description string) (run.Descriptor, error) {
	inputSchema, err := jsonschema.For[Input]()
	if err != nil {
		return run.Descriptor{}, fmt.Errorf("%w: input schema: %w", ErrInvalidConfig, err)
	}
	outputSchema, err := jsonschema.For[Output]()
	if err != nil {
		return run.Descriptor{}, fmt.Errorf("%w: output schema: %w", ErrInvalidConfig, err)
	}
	signalSchema, err := jsonschema.For[steerSignal]()
	if err != nil {
		return run.Descriptor{}, fmt.Errorf("%w: signal schema: %w", ErrInvalidConfig, err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name:         name,
		Description:  description,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		SignalSchema: signalSchema,
	})
	if err != nil {
		return run.Descriptor{}, fmt.Errorf("%w: descriptor: %w", ErrInvalidConfig, err)
	}
	return descriptor, nil
}

func indexDelegates(delegates []Delegate, tools toolManifest) (map[string]int, error) {
	names := make(map[string]int, len(delegates))
	for index, delegate := range delegates {
		if !delegate.Valid() {
			return nil, fmt.Errorf("%w: Delegates[%d]: %w", ErrInvalidConfig, index, ErrInvalidDelegate)
		}
		name := delegate.definition.Name
		if _, duplicate := tools.entries[name]; duplicate {
			return nil, fmt.Errorf("%w: Delegate name %q collides with a Tool", ErrInvalidConfig, name)
		}
		if _, duplicate := names[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Delegate name %q", ErrInvalidConfig, name)
		}
		names[name] = index
	}
	return names, nil
}

// toolWindow sizes the Tool batch that starts at the first remaining call:
// the longest concurrent run the ToolSet admits, or one call when Tool calls
// never overlap. Step sizes a batch with it and Restore requires it.
func (d *definition) toolWindow(ctx context.Context, remaining []chat.ToolCall) (int, error) {
	if d.maxConcurrentToolCalls == 1 {
		return 1, nil
	}
	return d.tools.concurrentBatchEnd(ctx, remaining)
}

func (d *definition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	var children []run.Program[run.Payload, run.Payload]
	for _, delegate := range d.delegates {
		children = append(children, delegate.child.Program)
	}
	return children
}

func (d *definition) Descriptor() run.Descriptor {
	return d.descriptor
}

func (d *definition) Start(input run.Payload) (run.Execution, error) {
	decoded, err := input.Decode[Input]()
	if err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrInvalidInput, err)
	}
	if err := decoded.Validate(); err != nil {
		return nil, err
	}
	return &execution{
		definition: d,
		state:      executionState{WorkingContext: &decoded},
	}, nil
}

// Restore recreates an Interaction solely from its opaque state. It accepts
// the current Interaction state schema and rejects unknown fields.
func (d *definition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	decoded, err := restore.Decode(ctx, state, executionStateKind, ErrInvalidExecutionState, func(ctx context.Context, decoded executionState) error {
		return decoded.validate(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	return &execution{definition: d, state: decoded}, nil
}

func (d *definition) delegate(name string) (Delegate, bool) {
	index, found := d.delegatesByName[name]
	if !found {
		return Delegate{}, false
	}
	return d.delegates[index], true
}

var _ run.Definition = (*definition)(nil)

// modelDefinitions reads advertisedToolNames as the state owns them: Restore
// and every merge already admitted them against this manifest.
func (d *definition) modelDefinitions(advertisedToolNames []string) []tool.Definition {
	tools := d.tools
	definitions := tools.initialDefinitions()
	for _, delegate := range d.delegates {
		definitions = append(definitions, delegate.definition.Clone())
	}
	for _, name := range advertisedToolNames {
		definitions = append(definitions, tools.entries[name].modelDefinition())
	}
	return definitions
}

func (d *definition) settleModelResponse(response *chat.Response, cause error) (run.Settlement, error) {
	var outcome run.Settlement
	var err error
	if cause == nil {
		outcome, err = (modelCallResult{Response: response}).settlement()
	} else {
		outcome, err = modelFailureSettlement(response, cause)
	}
	if err != nil {
		return run.Settlement{}, err
	}
	if err := d.validateResponsePayload(outcome.Payload()); err != nil {
		return run.Settlement{}, errors.Join(err, cause)
	}
	return outcome, nil
}

func (d *definition) validateResponsePayload(payload run.Payload) error {
	if len(payload.JSON()) > d.maxResponseBytes {
		return ErrModelResponseTooLarge
	}
	return nil
}

func (d *definition) settlePreparedContext(req run.EffectRequest, messages []chat.Message) (run.Settlement, error) {
	if !d.prepareContext {
		return run.Settlement{}, ErrInvalidConfig
	}
	if !req.Valid() {
		return run.Settlement{}, ErrInvalidProtocol
	}
	env, err := decodeEffect(req.Effect().Payload().JSON())
	if err != nil {
		return run.Settlement{}, err
	}
	if env.operation() != operationPrepare {
		return run.Settlement{}, ErrInvalidProtocol
	}
	effective := env.Preparation.request()
	effective.Messages = cloneMessages(messages)
	if err = effective.Validate(); err != nil {
		return run.Settlement{}, err
	}
	payload, err := run.EncodePayload(resultEnvelope{Prepared: &preparedContext{Messages: effective.Messages}})
	if err != nil {
		return run.Settlement{}, err
	}
	if err := d.validateResponsePayload(payload); err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload.JSON())
}

// Admission examines the complete model proposal before any child can execute.
// A later invalid argument must not strand effects from an earlier sibling.
func (d *definition) admitProposals(ctx context.Context, calls []chat.ToolCall, advertised []string) (string, error) {
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var message string
		if delegate, ok := d.delegate(call.Name); ok {
			if _, err := delegate.prepareInput(call); err != nil {
				message = err.Error()
			}
		} else {
			entry, found := d.tools.entries[call.Name]
			if !found || (entry.deferred && !slices.Contains(advertised, call.Name)) {
				return fmt.Sprintf("tool batch was not executed: tool %q is not available", call.Name), nil
			}
			if _, err := entry.prepare(call, tool.Admission{}); err != nil {
				rejection, public := tool.ClassifyInputRejection(err)
				if !public {
					return "", errToolInputValidatorDefect
				}
				message = rejection.Message()
			}
		}
		if message != "" {
			return fmt.Sprintf("tool batch was not executed: %q has invalid arguments: %s", call.Name, run.NormalizeDiagnostic(message)), nil
		}
	}
	return "", nil
}
