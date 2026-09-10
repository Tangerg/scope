package interaction

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// ModelClient stays consumer-owned so Interaction does not depend on one
// concrete Core client implementation.
type ModelClient interface {
	// Call invokes the configured model for one complete response.
	Call(ctx context.Context, request *chat.Request) (*chat.Response, error)
}

// ModelResponseMode selects the single model response lifecycle used by a
// Dispatcher. Streaming is observational; both modes settle the same complete
// provider-neutral Response.
type ModelResponseMode string

// Complete mode receives one response; stream mode accumulates response events
// before settling the same complete response contract.
const (
	ModelResponseComplete ModelResponseMode = ""
	ModelResponseStream   ModelResponseMode = "stream"
)

func (m ModelResponseMode) Valid() bool {
	return m == ModelResponseComplete || m == ModelResponseStream
}

// DispatcherConfig binds external capabilities for one Deployment.
type DispatcherConfig struct {
	// Client is the single model dependency. Stream mode requires the same value
	// to implement chat.Streamer so call and stream settings cannot diverge.
	Client ModelClient
	// ResponseMode selects complete or streaming model responses.
	ResponseMode ModelResponseMode

	// Observer receives exact model response facts. Nil disables observation.
	// It is separate from Engine Events/Deltas, which describe execution mechanics.
	Observer ModelObserver

	// ModelContextReducer optionally replaces only the provider-neutral message
	// context at the last safe boundary before each model call. The Dispatcher
	// installs the effective messages back into Interaction recovery state when
	// the call settles, so later calls and checkpoints cannot regrow a reduced
	// context from the pre-reduction Effect payload.
	ModelContextReducer ModelContextReducer
}

// Dispatcher executes model calls emitted by an Interaction Execution. Its configuration is immutable after construction;
// internal observation health counters are concurrency-safe. It may serve
// Processes concurrently when the supplied Client supports concurrent use.
type Dispatcher struct {
	client              ModelClient
	streamer            chat.Streamer
	initialDefinitions  []chat.ToolDefinition
	deferredDefinitions map[string]chat.ToolDefinition
	observer            ModelObserver
	observationFailures observationFailureCounters
	contextReducer      ModelContextReducer
}

// ObservationFailures returns a concurrency-safe snapshot of ModelObserver
// panics isolated by this Dispatcher. The counts do not alter settlements.
func (d *Dispatcher) ObservationFailures() ObservationFailureCounts {
	if d == nil {
		return ObservationFailureCounts{}
	}
	return d.observationFailures.snapshot()
}

// NewDispatcher binds the model boundary to its immutable Interaction manifest.
// Ordinary Tools run in the separate Deployment supplied by Definition's ToolSet.
func NewDispatcher(definition *Definition, config DispatcherConfig) (*Dispatcher, error) {
	if !definition.valid() || lo.IsNil(config.Client) {
		return nil, fmt.Errorf("%w: Definition and Client are required", ErrInvalidDispatcherConfig)
	}
	if !config.ResponseMode.Valid() {
		return nil, fmt.Errorf("%w: invalid ResponseMode %q", ErrInvalidDispatcherConfig, config.ResponseMode)
	}
	if config.Observer != nil && lo.IsNil(config.Observer) {
		return nil, fmt.Errorf("%w: Observer is typed nil", ErrInvalidDispatcherConfig)
	}
	if config.ModelContextReducer != nil && lo.IsNil(config.ModelContextReducer) {
		return nil, fmt.Errorf("%w: ModelContextReducer is typed nil", ErrInvalidDispatcherConfig)
	}
	var streamer chat.Streamer
	if config.ResponseMode == ModelResponseStream {
		streamer, _ = config.Client.(chat.Streamer)
		if lo.IsNil(streamer) {
			return nil, fmt.Errorf("%w: Client does not support streaming", ErrInvalidDispatcherConfig)
		}
	}
	dispatcher := &Dispatcher{
		client: config.Client, streamer: streamer, observer: config.Observer,
		contextReducer:      config.ModelContextReducer,
		initialDefinitions:  cloneDefinitions(definition.tools.initialDefinitions),
		deferredDefinitions: make(map[string]chat.ToolDefinition),
	}
	for name, entry := range definition.tools.entries {
		if entry.deferred {
			dispatcher.deferredDefinitions[name] = entry.definition.Clone()
		}
	}
	for _, delegate := range definition.delegates {
		dispatcher.initialDefinitions = append(dispatcher.initialDefinitions, delegate.definition.Clone())
	}
	return dispatcher, nil
}

// Dispatch executes one validated Interaction protocol operation and returns a
// definite owner-defined Signal payload. An error means the external outcome
// is not provable; Engine therefore records an unknown settlement instead of
// retrying the operation.
func (d *Dispatcher) Dispatch(
	ctx context.Context,
	request agent.EffectRequest,
	emit agent.DeltaEmitter,
) (agent.Settlement, error) {
	if d == nil || lo.IsNil(d.client) {
		return agent.Settlement{}, ErrInvalidDispatcherConfig
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return agent.Settlement{}, err
	}
	switch envelope.Operation {
	case operationModelCall:
		return d.dispatchModel(ctx, request, envelope.ModelCall, emit)
	default:
		return agent.Settlement{}, errors.New("interaction: unsupported dispatcher operation")
	}
}

// ReplayPolicy is deliberately conservative: model calls may incur cost and
// produce a different answer. Recovery requires explicit Process resolution.
func (*Dispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicyNever
}

func (d *Dispatcher) dispatchModel(
	ctx context.Context,
	request agent.EffectRequest,
	call *modelCall,
	emit agent.DeltaEmitter,
) (agent.Settlement, error) {
	modelRequest := call.Request.Clone()
	definitions, err := d.modelDefinitions(call.AdvertisedToolNames)
	if err != nil {
		return agent.Settlement{}, err
	}
	modelRequest.Tools = definitions
	if validateErr := modelRequest.Validate(); validateErr != nil {
		return agent.Settlement{}, fmt.Errorf("interaction: prepare model request: %w", validateErr)
	}
	invocation := modelInvocationFromRequest(
		request,
		call.ModelCallSequence,
		call.AppliedSteerSignalIDs,
	)
	ctx = withModelInvocation(ctx, invocation)
	if d.contextReducer != nil {
		effectiveMessages, reduceErr := d.contextReducer.ReduceModelContext(
			ctx, invocation, modelRequest.Clone(),
		)
		if reduceErr != nil {
			return modelHostFailureSettlement(
				request.ID(),
				fmt.Errorf("interaction: reduce model context: %w", reduceErr),
			)
		}
		modelRequest.Messages = cloneMessages(effectiveMessages)
		if validateErr := modelRequest.Validate(); validateErr != nil {
			return modelHostFailureSettlement(
				request.ID(),
				fmt.Errorf("interaction: reduced model context: %w", validateErr),
			)
		}
	}
	response, err := d.callModel(ctx, modelRequest, emit)
	if err != nil {
		if errors.Is(err, ErrHostFailure) {
			return modelHostFailureSettlement(request.ID(), err)
		}
		return modelFailureSettlement(request.ID(), err)
	}
	if response == nil {
		return modelFailureSettlement(request.ID(), errors.New("model returned a nil response"))
	}
	if validateErr := response.Validate(); validateErr != nil {
		return modelFailureSettlement(request.ID(), fmt.Errorf("invalid model response: %w", validateErr))
	}
	d.observeModel(ctx, invocation, response)
	result := &modelCallResult{Response: response.Clone()}
	if d.contextReducer != nil && !reflect.DeepEqual(call.Request.Messages, modelRequest.Messages) {
		result.ReplacementMessages = cloneMessages(modelRequest.Messages)
	}
	payload, err := encodeProtocol(signalEnvelope{
		Operation: operationModelCall, ModelResult: result,
	})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(request.ID(), agent.SettlementStatusSucceeded, payload)
}

func (d *Dispatcher) modelDefinitions(advertisedToolNames []string) ([]chat.ToolDefinition, error) {
	if err := validateAdvertisedToolNames(advertisedToolNames); err != nil {
		return nil, fmt.Errorf("interaction: advertised Tools: %w", err)
	}
	definitions := cloneDefinitions(d.initialDefinitions)
	for _, name := range advertisedToolNames {
		definition, found := d.deferredDefinitions[name]
		if !found {
			return nil, fmt.Errorf("interaction: tool %q is not a bound deferred Tool", name)
		}
		definitions = append(definitions, definition.Clone())
	}
	return definitions, nil
}

func modelFailureSettlement(effectID agent.EffectID, cause error) (agent.Settlement, error) {
	payload, err := encodeProtocol(signalEnvelope{
		Operation:   operationModelCall,
		ModelResult: &modelCallResult{Error: boundedDiagnostic(cause.Error())},
	})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(effectID, agent.SettlementStatusFailed, payload)
}

func modelHostFailureSettlement(effectID agent.EffectID, cause error) (agent.Settlement, error) {
	payload, err := encodeProtocol(signalEnvelope{
		Operation:   operationModelCall,
		ModelResult: &modelCallResult{HostError: boundedDiagnostic(cause.Error())},
	})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(effectID, agent.SettlementStatusFailed, payload)
}

func cloneDefinitions(definitions []chat.ToolDefinition) []chat.ToolDefinition {
	cloned := make([]chat.ToolDefinition, len(definitions))
	for index := range definitions {
		cloned[index] = definitions[index].Clone()
	}
	return cloned
}

var _ agent.Dispatcher = (*Dispatcher)(nil)
