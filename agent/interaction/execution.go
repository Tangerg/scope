package interaction

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

type execution struct {
	definition *definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, turn run.Turn) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch e.state.phase() {
	case phaseReadyModel:
		return e.startModelCall(ctx, turn)
	case phaseAdvancingTools, phaseRoundComplete:
		return e.continueToolRound(ctx, turn)
	case phaseAwaitingContext:
		return e.acceptContext(turn)
	case phaseAwaitingModel:
		return e.acceptModel(ctx, turn)
	case phaseAwaitingCalls:
		return e.advanceCalls(turn)
	default:
		return run.Transition{}, ErrInvalidExecutionState
	}
}

func (e *execution) startModelCall(ctx context.Context, turn run.Turn) (run.Transition, error) {
	consumed, err := e.acceptSteer(turn.Messages)
	if err != nil {
		return run.Transition{}, err
	}
	appliedSteerSignalIDs, err := e.state.applyPendingSteer()
	if err != nil {
		return run.Transition{}, err
	}
	return e.requestModel(ctx, turn, consumed, appliedSteerSignalIDs)
}

func (e *execution) continueToolRound(ctx context.Context, turn run.Turn) (run.Transition, error) {
	consumed, err := e.acceptSteer(turn.Messages)
	if err != nil {
		return run.Transition{}, err
	}
	if e.state.phase() == phaseRoundComplete {
		return e.finishToolCallBatch(ctx, turn, consumed, e.state.ToolRound.Response.Output.Message)
	}
	return e.advanceToolCallBatch(ctx, turn, consumed)
}

func (e *execution) acceptSteer(signals []run.Message) (uint32, error) {
	steer, consumed, err := collectSteerSignals(signals)
	if err != nil {
		return 0, err
	}
	e.state.addSteer(steer)
	return consumed, nil
}

func (e *execution) Snapshot() (run.ExecutionState, error) {
	return e.state.snapshot()
}

func (e *execution) requestModel(
	ctx context.Context, turn run.Turn, consumedMessages uint32,
	appliedSteerSignalIDs []run.SignalID,
) (run.Transition, error) {
	if !e.definition.maxModelCalls.Allows(e.state.ModelCallCount, 1) {
		return stepfail.Transition(
			consumedMessages,
			run.FailureKindExecution,
			failureCodeInteractionLimitModelCalls,
			"Interaction reached its configured model-call limit before a final response",
		)
	}
	if e.state.ModelCallCount == math.MaxUint64 {
		return run.Transition{}, run.ErrCounterExhausted
	}
	if e.definition.contextPolicy != nil {
		messages, err := e.definition.contextPolicy(ctx, *e.state.WorkingContext.clone())
		if contextErr := ctx.Err(); contextErr != nil {
			return run.Transition{}, contextErr
		}
		if err != nil {
			return stepfail.Transition(consumedMessages, run.FailureKindExecution, failureCodeInteractionContextPolicyFailed, err.Error())
		}
		if err := e.state.replaceModelContext(cloneMessages(messages)); err != nil {
			return run.Transition{}, err
		}
	}
	return e.emitModel(turn, consumedMessages, appliedSteerSignalIDs, e.definition.prepareContext)
}

func (e *execution) emitModel(turn run.Turn, consumedMessages uint32, appliedSteerSignalIDs []run.SignalID, prepare bool) (run.Transition, error) {
	modelCallSequence := e.state.ModelCallCount + 1
	envelope, err := newModelEffect(e.state.WorkingContext, modelCallSequence, e.definition.modelDefinitions(e.state.AdvertisedToolNames), appliedSteerSignalIDs)
	if err != nil {
		return run.Transition{}, err
	}
	if prepare {
		envelope.Preparation, envelope.ModelCall = envelope.ModelCall, nil
	}
	payload, err := run.EncodePayload(envelope)
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewDispatcherEffect(payload)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.PendingEffect = turn.EffectID(0)
	if prepare {
		e.state.Preparation = &contextPreparation{Applied: slices.Clone(appliedSteerSignalIDs)}
	} else {
		e.state.ModelCallCount = modelCallSequence
	}
	return run.Yield(consumedMessages, []run.Effect{effect})
}

func (e *execution) acceptContext(turn run.Turn) (run.Transition, error) {
	arrival, err := collectEffectResult(turn.Messages, operationPrepare, e.state.PendingEffect)
	if err != nil {
		return run.Transition{}, err
	}
	if !arrival.settled {
		return e.awaitPendingEffect(arrival)
	}
	e.state.PendingEffect = run.EffectID{}
	if arrival.failure.Valid() {
		return run.Fail(arrival.consumed, arrival.failure)
	}
	if err = e.state.replaceModelContext(arrival.result.Prepared.Messages); err != nil {
		return run.Transition{}, err
	}
	applied := e.state.Preparation.Applied
	e.state.Preparation = nil
	e.state.addSteer(arrival.steer)
	extra, err := e.state.applyPendingSteer()
	if err != nil {
		return run.Transition{}, err
	}
	return e.emitModel(turn, arrival.consumed, append(applied, extra...), false)
}

// awaitPendingEffect keeps steering that arrived before the awaited settlement.
func (e *execution) awaitPendingEffect(arrival effectArrival) (run.Transition, error) {
	e.state.addSteer(arrival.steer)
	return run.Yield(arrival.consumed, nil)
}

func (e *execution) acceptModel(ctx context.Context, turn run.Turn) (run.Transition, error) {
	arrival, err := collectEffectResult(turn.Messages, operationModelCall, e.state.PendingEffect)
	if err != nil {
		return run.Transition{}, err
	}
	if !arrival.settled {
		return e.awaitPendingEffect(arrival)
	}
	consumedMessages := arrival.consumed
	e.state.PendingEffect = run.EffectID{}
	if arrival.failure.Valid() {
		return run.Fail(consumedMessages, arrival.failure)
	}
	response := arrival.result.ModelResult.Response.Clone()
	calls := proposedCalls(response)
	if len(calls) > 0 && !opensToolRound(response.Output.FinishReason) {
		return stepfail.Transition(
			consumedMessages,
			run.FailureKindExternal,
			failureCodeInteractionModelToolCallsNotCompleted,
			fmt.Sprintf("model output ended with %q; tool calls were not executed", response.Output.FinishReason),
		)
	}
	e.state.addSteer(arrival.steer)
	if len(calls) == 0 {
		return e.acceptFinalModelResponse(ctx, turn, consumedMessages, response)
	}
	round := &toolCallRound{Response: response}
	if round.truncated() {
		for range calls {
			round.reject("tool call was not executed because model output reached its token limit; emit the complete call again")
		}
	} else {
		rejection, err := e.definition.admitProposals(ctx, calls, e.state.AdvertisedToolNames)
		if err != nil {
			return run.Transition{}, err
		}
		if rejection != "" {
			for range calls {
				round.reject(rejection)
			}
		}
	}
	e.state.ToolRound = round
	return run.Continue(consumedMessages)
}

func (e *execution) acceptFinalModelResponse(
	ctx context.Context,
	turn run.Turn,
	consumedMessages uint32,
	response *chat.Response,
) (run.Transition, error) {
	message := finishedMessage(response)
	if message == nil {
		return run.Transition{}, fmt.Errorf("%w: model response has no finished assistant message", ErrInvalidModelResponse)
	}
	if e.state.PendingSteer == nil {
		return e.finishOrRetry(ctx, turn, consumedMessages, Output{
			ModelResponse: response,
			ModelCalls:    e.state.ModelCallCount,
		}, []chat.Message{message.Clone()})
	}
	if err := e.state.appendToContext("steered final response", *message); err != nil {
		return run.Transition{}, err
	}
	appliedSteerSignalIDs, err := e.state.applyPendingSteer()
	if err != nil {
		return run.Transition{}, err
	}
	return e.requestModel(ctx, turn, consumedMessages, appliedSteerSignalIDs)
}

// complete admits an Output by the canonical payload encoding the Runtime
// delivers, so a response too large to deliver becomes a definite failure here
// rather than an opaque encode error that strands the Interaction. finishOrRetry
// already validated the Output, so size is the only encode failure left.
func (e *execution) complete(consumedMessages uint32, output Output) (run.Transition, error) {
	encoded, err := run.EncodePayload(output)
	if err != nil {
		if errors.Is(err, run.ErrPayloadTooLarge) {
			return stepfail.Transition(
				consumedMessages,
				run.FailureKindExecution,
				failureCodeInteractionLimitResponseBytes,
				"final response exceeds the deliverable payload size limit",
			)
		}
		return run.Transition{}, err
	}
	return run.Complete(consumedMessages, encoded)
}

func (e *execution) finishOrRetry(
	ctx context.Context,
	turn run.Turn,
	consumedMessages uint32,
	output Output,
	completionContext []chat.Message,
) (run.Transition, error) {
	if err := output.Validate(); err != nil {
		return run.Transition{}, err
	}
	decision := CompletionDecision{Accepted: true}
	if e.definition.completionValidator != nil {
		candidate := CompletionCandidate{
			workingContext: e.state.WorkingContext.request(),
			output:         output.clone(),
			artifacts:      newArtifacts(e.state.ArtifactRecords),
		}
		var err error
		decision, err = e.definition.completionValidator(ctx, candidate)
		if cancelErr := ctx.Err(); cancelErr != nil {
			return run.Transition{}, cancelErr
		}
		if err != nil {
			return stepfail.Transition(
				consumedMessages,
				run.FailureKindExecution,
				failureCodeInteractionCompletionValidatorFailed,
				err.Error(),
			)
		}
	}
	if !decision.Valid() {
		return stepfail.Transition(
			consumedMessages,
			run.FailureKindContract,
			failureCodeInteractionCompletionDecisionInvalid,
			"CompletionValidator returned an invalid decision",
		)
	}
	if decision.Accepted {
		return e.complete(consumedMessages, output)
	}
	feedback := chat.NewUserMessage(chat.NewTextPart(decision.Feedback))
	if err := e.state.appendToContext("completion retry request", slices.Concat(completionContext, []chat.Message{feedback})...); err != nil {
		return run.Transition{}, err
	}
	return e.requestModel(ctx, turn, consumedMessages, nil)
}

func (e *execution) advanceToolCallBatch(ctx context.Context, turn run.Turn, consumedMessages uint32) (run.Transition, error) {
	calls := e.state.ToolRound.calls()
	call := calls[e.state.ToolRound.nextCallIndex()]
	if _, delegated := e.definition.delegate(call.Name); delegated {
		effects, prepareErr := e.prepareDelegateCalls(ctx, turn, calls)
		if prepareErr != nil {
			return run.Transition{}, prepareErr
		}
		return run.Yield(consumedMessages, effects)
	}
	return e.startToolCalls(ctx, turn, consumedMessages, calls)
}

func (e *execution) finishToolCallBatch(
	ctx context.Context,
	turn run.Turn,
	consumedMessages uint32,
	assistant *chat.Message,
) (run.Transition, error) {
	calls := e.state.ToolRound.calls()
	results := make([]chat.ToolResult, len(e.state.ToolRound.Results))
	direct := e.state.PendingSteer == nil
	for index, result := range e.state.ToolRound.Results {
		results[index] = result.toolResult(calls[index])
		direct = direct && result.Disposition == ResultSucceeded && e.definition.tools.completesDirectly(calls[index].Name)
	}
	completionContext := []chat.Message{assistant.Clone(), chat.NewToolMessage(results...)}
	if err := e.state.recordRoundArtifacts(e.definition); err != nil {
		return run.Transition{}, err
	}
	e.state.ToolRound = nil
	if direct {
		return e.finishOrRetry(ctx, turn, consumedMessages, Output{
			DirectToolResults: results,
			ModelCalls:        e.state.ModelCallCount,
		}, completionContext)
	}
	if err := e.state.appendToContext("continuation request", completionContext...); err != nil {
		return run.Transition{}, err
	}
	appliedSteerSignalIDs, err := e.state.applyPendingSteer()
	if err != nil {
		return run.Transition{}, err
	}
	return e.requestModel(ctx, turn, consumedMessages, appliedSteerSignalIDs)
}

func collectSteerSignals(messages []run.Message) (steerBatch, uint32, error) {
	var batch steerBatch
	for _, message := range messages {
		recognized, err := batch.collectSignal(message)
		if err != nil {
			return steerBatch{}, 0, err
		}
		if !recognized {
			return steerBatch{}, 0, ErrInvalidSteer
		}
	}
	return batch, uint32(len(messages)), nil
}

// effectArrival is what one Step received while awaiting a pending Effect.
// Steering may arrive without the settlement; failure is valid only when the
// settlement definitely failed.
type effectArrival struct {
	settled  bool
	result   resultEnvelope
	failure  run.Failure
	steer    steerBatch
	consumed uint32
}

func collectEffectResult(messages []run.Message, expected operation, id run.EffectID) (effectArrival, error) {
	arrival := effectArrival{consumed: uint32(len(messages))}
	for _, message := range messages {
		switch value := message.(type) {
		case run.Signal:
			if err := arrival.steer.collectInput(value); err != nil {
				return effectArrival{}, err
			}
		case run.Completion:
			if arrival.settled || value.Effect != id {
				return effectArrival{}, ErrInvalidProtocol
			}
			envelope, failure, err := decodeSettlement(value.Outcome, expected)
			if err != nil {
				return effectArrival{}, err
			}
			arrival.settled, arrival.result, arrival.failure = true, envelope, failure
		default:
			return effectArrival{}, ErrInvalidProtocol
		}
	}
	return arrival, nil
}

func (e *execution) prepareDelegateCalls(ctx context.Context, turn run.Turn, calls []chat.ToolCall) ([]run.Effect, error) {
	start := e.state.ToolRound.nextCallIndex()
	batch := &callBatch{Kind: callDelegate}
	var effects []run.Effect
	for index := start; index < uint32(len(calls)); index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		call := calls[index]
		delegate, ok := e.definition.delegate(call.Name)
		if !ok {
			break
		}
		spec, err := delegate.childSpec(e.state.ModelCallCount, call)
		if err != nil {
			return nil, fmt.Errorf("%w: admitted delegation: %w", ErrInvalidExecutionState, err)
		}
		invocation, effect, err := child.Start(turn, uint32(len(effects)), spec)
		if err != nil {
			return nil, err
		}
		batch.Invocations = append(batch.Invocations, &callInvocation{Reference: turn.EffectID(uint32(len(effects))), Child: &invocation})
		effects = append(effects, effect)
	}
	if len(effects) == 0 {
		return nil, ErrInvalidExecutionState
	}
	e.state.ToolRound.beginCalls(batch)
	return effects, nil
}

func (e *execution) startToolCalls(ctx context.Context, turn run.Turn, consumed uint32, calls []chat.ToolCall) (run.Transition, error) {
	count, err := e.definition.toolWindow(ctx, calls[e.state.ToolRound.nextCallIndex():])
	if err != nil {
		return run.Transition{}, err
	}
	e.state.ToolRound.beginCalls(&callBatch{Kind: callTool, Invocations: make([]*callInvocation, count)})
	return e.scheduleTools(turn, consumed, nil)
}

func (e *execution) scheduleTools(turn run.Turn, consumed uint32, effects []run.Effect) (run.Transition, error) {
	calls := e.state.ToolRound.batchCalls()
	batch := e.state.ToolRound.Batch
	active := batch.active()
	for index, invocation := range batch.Invocations {
		if invocation != nil || active >= e.definition.maxConcurrentToolCalls {
			continue
		}
		effect, effectErr := toolEffect(toolDispatchRequest{Invocation: toolCall{Call: calls[index]}})
		if effectErr != nil {
			return run.Transition{}, effectErr
		}
		id := turn.EffectID(uint32(len(effects)))
		batch.Invocations[index] = &callInvocation{Reference: id, Pending: &id}
		effects = append(effects, effect)
		active++
	}
	if active == 0 {
		names, finishErr := e.state.ToolRound.finishCalls(e.definition.tools, e.state.AdvertisedToolNames)
		if finishErr != nil {
			return run.Transition{}, finishErr
		}
		e.state.AdvertisedToolNames = names
		return run.Continue(consumed)
	}
	waits, err := batch.waits()
	if err != nil {
		return run.Transition{}, err
	}
	return run.Yield(consumed, effects, waits...)
}

func toolEffect(request toolDispatchRequest) (run.Effect, error) {
	envelope, err := newToolEffect(request)
	if err != nil {
		return run.Effect{}, err
	}
	payload, err := run.EncodePayload(envelope)
	if err != nil {
		return run.Effect{}, err
	}
	return run.NewDispatcherEffect(payload)
}

func (e *execution) advanceCalls(turn run.Turn) (run.Transition, error) {
	calls := e.state.ToolRound.batchCalls()
	var effects []run.Effect
	var steer steerBatch
	for offset, message := range turn.Messages {
		if recognized, steerErr := steer.collectSignal(message); steerErr != nil {
			return run.Transition{}, steerErr
		} else if recognized {
			continue
		}
		accepted := false
		for index, invocation := range e.state.ToolRound.Batch.Invocations {
			if invocation == nil || invocation.Result != nil {
				continue
			}
			matched, failure, acceptErr := e.acceptCall(turn, message, calls[index], invocation, &effects)
			if acceptErr != nil {
				return run.Transition{}, acceptErr
			}
			if failure != nil {
				return run.Fail(uint32(offset+1), *failure)
			}
			if matched {
				accepted = true
				break
			}
		}
		if !accepted {
			return run.Transition{}, ErrInvalidProtocol
		}
	}
	e.state.addSteer(steer)
	consumed := uint32(len(turn.Messages))
	if e.state.ToolRound.Batch.Kind == callTool {
		return e.scheduleTools(turn, consumed, effects)
	}
	if e.state.ToolRound.Batch.active() != 0 {
		return run.Yield(consumed, nil)
	}
	names, err := e.state.ToolRound.finishCalls(e.definition.tools, e.state.AdvertisedToolNames)
	if err != nil {
		return run.Transition{}, err
	}
	e.state.AdvertisedToolNames = names
	return run.Continue(consumed)
}

func (e *execution) acceptCall(turn run.Turn, message run.Message, call chat.ToolCall, invocation *callInvocation, effects *[]run.Effect) (bool, *run.Failure, error) {
	if invocation.Child != nil {
		matched, err := invocation.acceptDelegate(message)
		return matched, nil, err
	}
	switch value := message.(type) {
	case run.Signal:
		effect, matched, err := invocation.acceptAnswer(call, value, turn.EffectID(uint32(len(*effects))))
		if matched && err == nil {
			*effects = append(*effects, effect)
		}
		return matched, nil, err
	case run.Completion:
		return invocation.acceptToolSettlement(call, value)
	default:
		return false, nil, nil
	}
}

const (
	failureCodeInteractionToolAdvertisementInvalid   = "interaction.tool.advertisement_invalid"
	failureCodeInteractionCompletionDecisionInvalid  = "interaction.completion.decision_invalid"
	failureCodeInteractionCompletionValidatorFailed  = "interaction.completion.validator_failed"
	failureCodeInteractionContextPolicyFailed        = "interaction.context.policy_failed"
	failureCodeInteractionLimitModelCalls            = "interaction.limit.model_calls"
	failureCodeInteractionLimitResponseBytes         = "interaction.limit.response_bytes"
	failureCodeInteractionModelToolCallsNotCompleted = "interaction.model.tool_calls_not_completed"
)
