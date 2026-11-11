package interaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/invocation"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// dispatcher executes model calls emitted by an Interaction Execution. It is
// immutable after construction and may serve Processes concurrently when the
// supplied model capability supports concurrent use.
type dispatcher struct {
	tools                    ToolSet
	requiredToolCapabilities run.CapabilitySet
	model                    ModelSource
	// definition owns the Tool manifest and Delegates the model may call.
	definition       *definition
	contextProcessor ContextProcessor
}

// newDispatcher takes the executable half of config; definition already holds
// its declarations.
func newDispatcher(definition *definition, config Config) (*dispatcher, error) {
	switch {
	case lo.IsNil(config.Model):
		return nil, fmt.Errorf("%w: Model is required", ErrInvalidConfig)
	case config.ContextProcessor != nil && lo.IsNil(config.ContextProcessor):
		return nil, fmt.Errorf("%w: ContextProcessor is typed nil", ErrInvalidConfig)
	}
	if err := config.Model.validate(); err != nil {
		return nil, err
	}
	if definition.prepareContext != (config.ContextProcessor != nil) {
		return nil, fmt.Errorf("%w: preparation effect and ContextProcessor must be bound together", ErrInvalidConfig)
	}
	dispatcher := &dispatcher{
		tools: config.Tools, requiredToolCapabilities: config.RequiredToolCapabilities,
		model: config.Model, contextProcessor: config.ContextProcessor,

		definition: definition,
	}
	return dispatcher, nil
}

// Dispatch executes one validated Interaction protocol operation and returns an
// owner-defined settlement payload. An error means the external outcome
// is not provable; Runtime therefore records an unknown settlement instead of
// retrying the operation.
func (d *dispatcher) Dispatch(
	ctx context.Context,
	request run.EffectRequest,
	emit run.DeltaEmitter,
) (run.Settlement, error) {
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
	policy.Replay = d.tools.replayPolicy(envelope.ToolCall.Call.Name)
	policy.RequiredCapabilities = d.requiredToolCapabilities
	return policy
}

// dispatchModel sends the request decodeEffect already validated.
func (d *dispatcher) dispatchModel(ctx context.Context, call *modelCall, emit run.DeltaEmitter) (run.Settlement, error) {
	response, err := d.callModelResponse(ctx, call.request(), emit)
	return d.definition.settleModelResponse(response, err)
}

func (d *dispatcher) prepareContext(ctx context.Context, req run.EffectRequest, call *modelCall) (run.Settlement, error) {
	if d.contextProcessor == nil {
		return failedSettlement(ErrInvalidConfig)
	}
	messages, err := d.contextProcessor.PrepareContext(ctx, ContextInvocation{Effect: req, Sequence: call.ModelCallSequence}, call.request())
	if err != nil {
		if _, definite := errors.AsType[*ContextFailure](err); definite {
			return stepfail.Settlement(run.FailureKindExternal, failureCodeContextRejected, err.Error())
		}
		return run.Settlement{}, err
	}
	result, err := d.definition.settlePreparedContext(call, messages)
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
			return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: fmt.Errorf("interaction: invalid model response: %w", errors.Join(err, validateErr))}
		}
	}
	if (response != nil || accumulator.HasDeltas()) && invocation.ClassifyFailure(err) == invocation.FailureNotExecuted {
		err = &invocation.Failure{Kind: invocation.FailureUnknown, Cause: fmt.Errorf("interaction: model generation contradicts non-execution: %w", err)}
	}
	if err != nil {
		kind := invocation.ClassifyFailure(err)
		if kind == invocation.FailureFailed || kind == invocation.FailureNotExecuted {
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
			response = nil
			err = &invocation.Failure{Kind: invocation.FailureUnknown, Cause: &run.CallbackPanicError{Operation: d.model.operation(), Value: recovered}}
		}
	}()
	return d.model.respond(ctx, request, emit, accumulator)
}

var _ run.Dispatcher = (*dispatcher)(nil)
