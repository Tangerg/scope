package interaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// DefaultMaxStreamDeltaBytes keeps stream admission bounded independently of
// the final settlement size, including streams that never finish.
const DefaultMaxStreamDeltaBytes = 64 << 20

type dispatcherConfig struct {
	Tools                    ToolSet
	RequiredToolCapabilities run.CapabilitySet
	// Exactly one of Model and Streamer is required. The selected capability
	// owns the entire response lifecycle; streaming is accumulated before settlement.
	Model    chat.Model
	Streamer chat.Streamer

	// MaxStreamDeltaBytes bounds cumulative Scope-encoded ModelResponseDelta
	// payloads before accumulation, independently of MaxResponseBytes. Zero uses
	// DefaultMaxStreamDeltaBytes; a nonzero value requires Streamer.
	MaxStreamDeltaBytes int

	// ContextProcessor optionally replaces the message context immediately
	// before each model call. Nil sends WorkingContext unchanged.
	ContextProcessor ContextProcessor
}

// dispatcher executes model calls emitted by an Interaction Execution. It is
// immutable after construction and may serve Processes concurrently when the
// supplied model capability supports concurrent use.
type dispatcher struct {
	tools                    ToolSet
	requiredToolCapabilities run.CapabilitySet
	model                    chat.Model
	streamer                 chat.Streamer
	maxStreamDeltaBytes      int
	// definition owns the Tool manifest and Delegates the model may call.
	definition       *definition
	contextProcessor ContextProcessor
}

func (d dispatcherConfig) validate() error {
	switch {
	case (d.Model == nil) == (d.Streamer == nil):
		return fmt.Errorf("%w: exactly one of Model and Streamer is required", ErrInvalidDispatcherConfig)
	case lo.IsNil(d.Model) && lo.IsNil(d.Streamer):
		return fmt.Errorf("%w: model capability is typed nil", ErrInvalidDispatcherConfig)
	case d.MaxStreamDeltaBytes < 0:
		return fmt.Errorf("%w: MaxStreamDeltaBytes must not be negative", ErrInvalidDispatcherConfig)
	case d.Streamer == nil && d.MaxStreamDeltaBytes != 0:
		return fmt.Errorf("%w: MaxStreamDeltaBytes requires Streamer", ErrInvalidDispatcherConfig)
	case d.ContextProcessor != nil && lo.IsNil(d.ContextProcessor):
		return fmt.Errorf("%w: ContextProcessor is typed nil", ErrInvalidDispatcherConfig)

	}
	return nil
}

func newDispatcher(definition *definition, config dispatcherConfig) (*dispatcher, error) {
	if !definition.valid() {
		return nil, fmt.Errorf("%w: definition is required", ErrInvalidDispatcherConfig)
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	if definition.prepareContext != (config.ContextProcessor != nil) {
		return nil, fmt.Errorf("%w: preparation effect and ContextProcessor must be bound together", ErrInvalidDispatcherConfig)
	}
	streamLimit := config.MaxStreamDeltaBytes
	if config.Streamer != nil && streamLimit == 0 {
		streamLimit = DefaultMaxStreamDeltaBytes
	}

	dispatcher := &dispatcher{
		tools: config.Tools, requiredToolCapabilities: config.RequiredToolCapabilities,
		model: config.Model, streamer: config.Streamer,
		contextProcessor: config.ContextProcessor, maxStreamDeltaBytes: streamLimit,

		definition: definition,
	}
	return dispatcher, nil
}

// Dispatch executes one validated Interaction protocol operation and returns an
// owner-defined settlement payload. An error means the external outcome
// is not provable; Runtime therefore records an unknown settlement instead of
// retrying the operation.
// A nil ctx is invalid and panics before the request is processed.
func (d *dispatcher) Dispatch(
	ctx context.Context,
	request run.EffectRequest,
	emit run.DeltaEmitter,
) (run.Settlement, error) {
	ctx = run.RequireContext(ctx)
	if d == nil || (lo.IsNil(d.model) && lo.IsNil(d.streamer)) {
		return failedSettlement(ErrInvalidDispatcherConfig)
	}
	envelope, err := decodeEffect(request.Effect().Payload())
	if err != nil {
		return failedSettlement(err)
	}
	switch envelope.operation() {
	case operationPrepare:
		return d.prepareContext(ctx, request, envelope.Preparation)
	case operationModelCall:
		return d.dispatchModel(ctx, envelope.ModelCall, emit)
	case operationToolCall:
		if !d.tools.Configured() {
			return failedSettlement(ErrInvalidToolSet)
		}
		return d.tools.dispatcher.Dispatch(ctx, request, emit)
	default:
		return failedSettlement(errors.New("interaction: unsupported dispatcher operation"))
	}
}

// Model and context calls cannot safely replay; each tool declares its own
// replay safety in its immutable binding.
func (d *dispatcher) Policy(effect run.Effect) run.EffectPolicy {
	policy := run.EffectPolicy{Replay: run.ReplayPolicyNever}
	envelope, err := decodeEffect(effect.Payload())
	if err != nil || envelope.ToolCall == nil || !d.tools.Configured() {
		return policy
	}
	policy.Replay = d.tools.manifest().entries[envelope.ToolCall.Invocation.Call.Name].replayPolicy()
	policy.RequiredCapabilities = d.requiredToolCapabilities
	return policy
}

func (d *dispatcher) dispatchModel(ctx context.Context, call *modelCall, emit run.DeltaEmitter) (run.Settlement, error) {
	modelRequest := call.request()
	if validateErr := modelRequest.Validate(); validateErr != nil {
		return failedSettlement(fmt.Errorf("interaction: prepare model request: %w", validateErr))
	}
	// A content-free stop is the smallest complete chat response. Measure it
	// with the same owner and representation as the eventual settlement.
	minimum := modelCallResult{Response: &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}}}
	if _, settlementErr := minimum.settlement(d.definition.maxResponseBytes); settlementErr != nil {
		return failedSettlement(settlementErr)
	}
	response, err := d.callModelResponse(ctx, modelRequest, emit)
	if err != nil {
		return modelFailureSettlement(response, err)
	}
	return (modelCallResult{Response: response}).settlement(d.definition.maxResponseBytes)
}

func (d *dispatcher) prepareContext(ctx context.Context, req run.EffectRequest, call *modelCall) (run.Settlement, error) {
	if d.contextProcessor == nil {
		return failedSettlement(ErrInvalidDispatcherConfig)
	}
	original := call.request()
	messages, err := d.contextProcessor.PrepareContext(ctx, ContextInvocation{Effect: req, Sequence: call.ModelCallSequence}, original.Clone())
	if err != nil {
		if _, definite := errors.AsType[*ContextFailure](err); definite {
			return failedSettlement(err)
		}
		return run.Settlement{}, err
	}
	result, err := d.definition.settlePreparedContext(req, messages)
	if err != nil {
		return failedSettlement(err)
	}
	return result, nil
}

func (d *dispatcher) callModelResponse(ctx context.Context, request *chat.Request, emit run.DeltaEmitter) (response *chat.Response, err error) {
	var accumulator chat.ResponseAccumulator
	response, err = d.callModel(ctx, request, emit, &accumulator)
	// Invalid trailing frames cannot retract terminal generation. A partial
	// stream is not a Response, but still contradicts non-execution.
	if response == nil && err != nil {
		response, _ = accumulator.Response()
	}
	if response != nil {
		if validateErr := response.Validate(); validateErr != nil {
			return nil, &chat.CallFailure{Kind: chat.CallFailureUnknown, Cause: fmt.Errorf("interaction: invalid model response: %w", errors.Join(err, validateErr))}
		}
	}
	if (response != nil || accumulator.HasDeltas()) && chat.ClassifyFailure(err) == chat.CallFailureNotExecuted {
		err = &chat.CallFailure{Kind: chat.CallFailureUnknown, Cause: fmt.Errorf("interaction: model generation contradicts non-execution: %w", err)}
	}
	if err != nil {
		kind := chat.ClassifyFailure(err)
		if kind == chat.CallFailureFailed || kind == chat.CallFailureNotExecuted {
			return response, err
		}
		return response, fmt.Errorf("interaction: model outcome unknown: %w", err)
	}
	if response == nil {
		return nil, errors.New("interaction: model outcome unknown: nil response")
	}
	return response, nil
}

func (d *dispatcher) callModel(
	ctx context.Context,
	request *chat.Request,
	emit run.DeltaEmitter,
	accumulator *chat.ResponseAccumulator,
) (response *chat.Response, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			operation := "chat.Model.Call"
			if d.streamer != nil {
				operation = "chat.Streamer.Stream"
			}
			response = nil
			err = &chat.CallFailure{Kind: chat.CallFailureUnknown, Cause: &run.CallbackPanicError{Operation: operation, Value: recovered}}
		}
	}()
	if d.streamer == nil {
		return d.model.Call(ctx, request)
	}
	sequence := d.streamer.Stream(ctx, request)
	if sequence == nil {
		return nil, errors.New("model streamer returned a nil sequence")
	}
	remainingDeltaBytes := d.maxStreamDeltaBytes
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
		if len(payload) > remainingDeltaBytes {
			return nil, ErrModelStreamTooLarge
		}
		remainingDeltaBytes -= len(payload)
		if err := accumulator.Add(delta); err != nil {
			return nil, fmt.Errorf("accumulate model stream: %w", err)
		}
		if emit != nil {
			emit(payload)
		}
	}
	// Response rejects a stream that produced no deltas, so no local tally is kept.
	response, err = accumulator.Response()
	if err != nil {
		return nil, fmt.Errorf("complete model stream: %w", err)
	}
	return response, nil
}

var _ run.Dispatcher = (*dispatcher)(nil)
