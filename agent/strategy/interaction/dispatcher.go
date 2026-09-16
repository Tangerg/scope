package interaction

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// DispatcherConfig binds external capabilities for one Deployment.
type DispatcherConfig struct {
	// ResultCommitter durably accepts exact, ordered results before model adoption.
	// Nil acknowledges in memory and provides no host persistence guarantee.
	ResultCommitter ResultCommitter
	// Exactly one of Model and Streamer is required. The selected capability
	// owns the entire response lifecycle; streaming is accumulated before settlement.
	Model    chat.Model
	Streamer chat.Streamer

	// MaxResponseBytes bounds both the complete encoded result envelope and the
	// cumulative encoded stream deltas, including replacement context. Stream
	// framing counts toward the cumulative limit. Zero uses agent.MaxPayloadBytes;
	// positive values may lower that limit. No partial response is promoted.
	MaxResponseBytes int

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
// Processes concurrently when the supplied model capability supports concurrent use.
type Dispatcher struct {
	resultCommitter     ResultCommitter
	model               chat.Model
	streamer            chat.Streamer
	initialDefinitions  []chat.ToolDefinition
	tools               toolManifest
	observer            ModelObserver
	observationFailures observationFailureCounters
	contextReducer      ModelContextReducer
	maxResponseBytes    int
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
	if !definition.valid() {
		return nil, fmt.Errorf("%w: Definition is required", ErrInvalidDispatcherConfig)
	}
	if (config.Model == nil) == (config.Streamer == nil) {
		return nil, fmt.Errorf("%w: exactly one of Model and Streamer is required", ErrInvalidDispatcherConfig)
	}
	if lo.IsNil(config.Model) && lo.IsNil(config.Streamer) {
		return nil, fmt.Errorf("%w: model capability is typed nil", ErrInvalidDispatcherConfig)
	}
	if config.Observer != nil && lo.IsNil(config.Observer) {
		return nil, fmt.Errorf("%w: Observer is typed nil", ErrInvalidDispatcherConfig)
	}
	if config.ResultCommitter != nil && lo.IsNil(config.ResultCommitter) {
		return nil, fmt.Errorf("%w: ResultCommitter is typed nil", ErrInvalidDispatcherConfig)
	}
	if config.ModelContextReducer != nil && lo.IsNil(config.ModelContextReducer) {
		return nil, fmt.Errorf("%w: ModelContextReducer is typed nil", ErrInvalidDispatcherConfig)
	}
	limit := config.MaxResponseBytes
	if limit < 0 || limit > agent.MaxPayloadBytes {
		return nil, fmt.Errorf("%w: MaxResponseBytes must be between 0 and %d", ErrInvalidDispatcherConfig, agent.MaxPayloadBytes)
	}
	if limit == 0 {
		limit = agent.MaxPayloadBytes
	}
	dispatcher := &Dispatcher{
		resultCommitter: config.ResultCommitter,
		model:           config.Model, streamer: config.Streamer, observer: config.Observer,
		contextReducer:     config.ModelContextReducer,
		maxResponseBytes:   limit,
		initialDefinitions: cloneDefinitions(definition.tools.initialDefinitions),
		tools:              definition.tools,
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
// A nil ctx is invalid and panics before the request is processed.
func (d *Dispatcher) Dispatch(
	ctx context.Context,
	request agent.EffectRequest,
	emit agent.DeltaEmitter,
) (agent.Settlement, error) {
	ctx = agent.RequireContext(ctx)
	if d == nil || (lo.IsNil(d.model) && lo.IsNil(d.streamer)) {
		return modelHostFailureSettlement(request.ID(), ErrInvalidDispatcherConfig)
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return modelHostFailureSettlement(request.ID(), err)
	}
	switch envelope.Operation {
	case operationResultCommit:
		batch, batchErr := newResultBatch(request, *envelope.ResultCommit)
		if batchErr != nil {
			return agent.Settlement{}, batchErr
		}
		receipt := batch.Receipt()
		if d.resultCommitter != nil {
			receipt, err = d.resultCommitter.CommitResults(ctx, batch)
			if err != nil {
				return agent.Settlement{}, fmt.Errorf("interaction: result commit outcome unknown: %w", err)
			}
		}
		if receipt != batch.Receipt() {
			return agent.Settlement{}, errors.New("interaction: result receipt does not match publication")
		}
		return receipt.Settlement()

	case operationModelCall:
		return d.dispatchModel(ctx, request, envelope.ModelCall, emit)
	default:
		return modelHostFailureSettlement(request.ID(), errors.New("interaction: unsupported dispatcher operation"))
	}
}

// ReplayPolicy permits only idempotent result publication. The committer must
// reconcile a previous transaction before writing. Models and Tools cannot be
// re-executed to repair publication; settled Unknowns require an explicit
// Process.ReplayUnknownEffect or a verified receipt via ResolveUnknownEffect.
func (*Dispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	if effect.Target() == agent.EffectTargetDispatcher {
		envelope, err := decodeEffect(effect.Payload())
		if err == nil && envelope.Operation == operationResultCommit {
			return agent.ReplayPolicySameIdentity
		}
	}
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
		return modelHostFailureSettlement(request.ID(), err)
	}
	modelRequest.Tools = definitions
	if validateErr := modelRequest.Validate(); validateErr != nil {
		return modelHostFailureSettlement(request.ID(), fmt.Errorf("interaction: prepare model request: %w", validateErr))
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
	result := &modelCallResult{}
	if d.contextReducer != nil && !reflect.DeepEqual(call.Request.Messages, modelRequest.Messages) {
		result.ReplacementMessages = cloneMessages(modelRequest.Messages)
	}
	base, err := jsonv2.Marshal(signalEnvelope{Operation: operationModelCall, ModelResult: result}, jsonv2.Deterministic(true))
	if err != nil {
		return modelHostFailureSettlement(request.ID(), err)
	}
	if len(base) >= d.maxResponseBytes {
		return modelHostFailureSettlement(request.ID(), ErrModelResponseTooLarge)
	}
	response, err := d.callModel(ctx, modelRequest, emit, d.maxResponseBytes-len(base))
	if err != nil {
		return agent.Settlement{}, fmt.Errorf("interaction: model outcome unknown: %w", err)
	}
	if response == nil {
		return agent.Settlement{}, errors.New("interaction: model outcome unknown: nil response")
	}
	if validateErr := response.Validate(); validateErr != nil {
		return agent.Settlement{}, fmt.Errorf("interaction: invalid model response: %w", validateErr)
	}
	result.Response = response
	payload, err := jsonv2.Marshal(signalEnvelope{
		Operation: operationModelCall, ModelResult: result,
	}, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Settlement{}, err
	}
	if len(payload) > d.maxResponseBytes {
		return agent.Settlement{}, ErrModelResponseTooLarge
	}
	d.observeModel(ctx, invocation, response)
	return agent.NewSettlement(request.ID(), agent.SettlementStatusSucceeded, payload)
}

func (d *Dispatcher) modelDefinitions(advertisedToolNames []string) ([]chat.ToolDefinition, error) {
	if err := d.tools.validateAdvertisements(advertisedToolNames); err != nil {
		return nil, fmt.Errorf("interaction: advertised Tools: %w", err)
	}
	definitions := cloneDefinitions(d.initialDefinitions)
	for _, name := range advertisedToolNames {
		definitions = append(definitions, d.tools.entries[name].contract.Definition())
	}
	return definitions, nil
}

func (d *Dispatcher) observeModel(ctx context.Context, invocation ModelInvocation, response *chat.Response) {
	if d.observer == nil {
		return
	}
	defer recordObserverPanic(&d.observationFailures.modelResponsePanics)
	d.observer.OnModelResponse(ctx, invocation, response.Clone())
}

func (d *Dispatcher) callModel(
	ctx context.Context,
	request *chat.Request,
	emit agent.DeltaEmitter,
	remainingBytes int,
) (*chat.Response, error) {
	if d.streamer == nil {
		return d.model.Call(ctx, request)
	}
	var accumulator chat.ResponseAccumulator
	seen := false
	sequence := d.streamer.Stream(ctx, request)
	if sequence == nil {
		return nil, errors.New("model streamer returned a nil sequence")
	}
	for delta, err := range sequence {
		if err != nil {
			return nil, err
		}
		if delta == nil {
			return nil, errors.New("model stream yielded a nil response Delta")
		}
		payload, err := encodeModelResponseDelta(delta)
		if err != nil {
			return nil, err
		}
		if len(payload) > remainingBytes {
			return nil, ErrModelResponseTooLarge
		}
		remainingBytes -= len(payload)
		if err := accumulator.Add(delta); err != nil {
			return nil, fmt.Errorf("accumulate model stream: %w", err)
		}
		seen = true
		if emit != nil {
			emit(payload)
		}
	}
	if !seen {
		return nil, errors.New("model stream ended without a response Delta")
	}
	response, err := accumulator.Response()
	if err != nil {
		return nil, fmt.Errorf("complete model stream: %w", err)
	}
	return response, nil
}

func modelHostFailureSettlement(effectID agent.EffectID, cause error) (agent.Settlement, error) {
	payload, err := jsonv2.Marshal(signalEnvelope{
		Operation:   operationModelCall,
		ModelResult: &modelCallResult{HostError: agent.NormalizeDiagnostic(cause.Error())},
	}, jsonv2.Deterministic(true))
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
