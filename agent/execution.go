package agent

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type execution struct {
	definition *Definition
	state      executionState
}

func (e *execution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if e == nil || !e.definition.valid() {
		return run.Transition{}, ErrInvalidExecutionState
	}
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch e.state.phase() {
	case phaseReadyModel:
		return e.startModelCall(ctx, signals)
	case phaseAdvancingTools, phaseRoundComplete:
		return e.continueToolRound(ctx, signals)
	case phaseAwaitingContext:
		return e.acceptContext(signals)
	case phaseAwaitingModel:
		return e.acceptModel(ctx, signals)
	case phaseAwaitingChildStarts:
		return e.acceptChildStarts(ctx, signals)
	case phaseAwaitingChildWaitOpen:
		return e.acceptChildWaitOpen(ctx, signals)
	case phaseWaitingChildren:
		return e.acceptChildCompletions(ctx, signals)
	case phaseCompleted:
		return run.Transition{}, fmt.Errorf("%w: completed execution cannot advance", ErrInvalidExecutionState)
	default:
		return run.Transition{}, ErrInvalidExecutionState
	}
}

func (e *execution) startModelCall(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	consumed, err := e.acceptSteer(signals)
	if err != nil {
		return run.Transition{}, err
	}
	appliedSteerSignalIDs, err := e.state.applyPendingSteer()
	if err != nil {
		return run.Transition{}, err
	}
	return e.requestModel(ctx, consumed, appliedSteerSignalIDs)
}

func (e *execution) continueToolRound(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	consumed, err := e.acceptSteer(signals)
	if err != nil {
		return run.Transition{}, err
	}
	if e.state.phase() == phaseRoundComplete {
		return e.finishToolCallBatch(ctx, consumed, e.state.ToolRound.Response.Output.Message)
	}
	return e.advanceToolCallBatch(ctx, consumed)
}

func (e *execution) acceptSteer(signals []run.Signal) (uint32, error) {
	steer, consumed, err := collectSteerSignals(signals)
	if err != nil {
		return 0, err
	}
	if err := e.state.addSteer(steer); err != nil {
		return 0, err
	}
	return consumed, nil
}

func (e *execution) Snapshot() (run.ExecutionState, error) {
	return e.state.snapshot()
}

func (e *execution) requestModel(
	ctx context.Context, consumedSignals uint32,
	appliedSteerSignalIDs []run.SignalID,
) (run.Transition, error) {
	if !e.definition.maxModelCalls.Allows(e.state.ModelCallCount, 1) {
		return stepfail.Transition(
			consumedSignals,
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
		if err != nil {
			return stepfail.Transition(consumedSignals, run.FailureKindExecution, failureCodeInteractionHostFailed, err.Error())
		}
		if err := e.state.replaceModelContext(messages); err != nil {
			return run.Transition{}, err
		}
	}
	return e.emitModel(consumedSignals, appliedSteerSignalIDs, e.definition.prepareContext)
}

func (e *execution) emitModel(consumedSignals uint32, appliedSteerSignalIDs []run.SignalID, prepare bool) (run.Transition, error) {
	modelCallSequence := e.state.ModelCallCount + 1
	envelope, err := newModelEffect(
		e.state.WorkingContext,
		modelCallSequence,
		e.state.AdvertisedToolNames,
		appliedSteerSignalIDs,
	)
	if err != nil {
		return run.Transition{}, err
	}
	if prepare {
		envelope.Preparation, envelope.ModelCall = envelope.ModelCall, nil
	}
	payload, err := jsonv2.Marshal(envelope, jsonv2.Deterministic(true))
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewDispatcherEffect(payload)
	if err != nil {
		return run.Transition{}, err
	}
	if prepare {
		e.state.Preparation = &contextPreparation{Applied: slices.Clone(appliedSteerSignalIDs)}
	} else {
		e.state.ModelCallCount = modelCallSequence
	}
	return run.Continue(consumedSignals, effect)
}

func (e *execution) acceptContext(signals []run.Signal) (run.Transition, error) {
	envelope, failure, steer, consumed, err := collectEffectResult(signals, operationPrepare)
	if err != nil {
		return run.Transition{}, err
	}
	if failure != "" {
		return stepfail.Transition(consumed, run.FailureKindExternal, failureCodeInteractionHostFailed, failure)
	}
	if err = e.state.replaceModelContext(envelope.Prepared.Messages); err != nil {
		return run.Transition{}, err
	}
	applied := e.state.Preparation.Applied
	e.state.Preparation = nil
	if err = e.state.addSteer(steer); err != nil {
		return run.Transition{}, err
	}
	extra, err := e.state.applyPendingSteer()
	if err != nil {
		return run.Transition{}, err
	}
	return e.emitModel(consumed, append(applied, extra...), false)
}

func (e *execution) acceptModel(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	envelope, hostFailure, steer, consumedSignals, err := collectEffectResult(signals, operationModelCall)
	if err != nil {
		return run.Transition{}, err
	}
	if hostFailure != "" {
		return stepfail.Transition(consumedSignals, run.FailureKindExternal, failureCodeInteractionHostFailed, hostFailure)
	}
	response := envelope.ModelResult.Response.Clone()
	calls, err := validatedToolCalls(response)
	if err != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", ErrInvalidModelResponse, err)
	}
	if len(calls) > 0 && !opensToolRound(response.Output.FinishReason) {
		return stepfail.Transition(
			consumedSignals,
			run.FailureKindExternal,
			failureCodeInteractionModelToolCallsNotCompleted,
			fmt.Sprintf("model output ended with %q; tool calls were not executed", response.Output.FinishReason),
		)
	}
	if addSteerErr := e.state.addSteer(steer); addSteerErr != nil {
		return run.Transition{}, addSteerErr
	}
	if len(calls) == 0 {
		return e.acceptFinalModelResponse(ctx, consumedSignals, response)
	}
	round := &toolCallRound{Response: response}
	if !round.truncated() {
		rejection, err := e.admitProposals(ctx, calls)
		if err != nil {
			return run.Transition{}, err
		}
		if rejection != "" {
			for _, call := range calls {
				round.reject(call, rejection)
			}
		}
	}
	e.state.ToolRound = round
	return run.Checkpoint(consumedSignals)
}

// Admission examines the complete model proposal before any child can execute.
// A later invalid argument must not strand effects from an earlier sibling.
func (e *execution) admitProposals(ctx context.Context, calls []chat.ToolCall) (string, error) {
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var err error
		if delegate, ok := e.definition.delegate(call.Name); ok {
			_, err = delegate.prepareInput(call)
		} else {
			entry, found := e.definition.tools.entries[call.Name]
			if !found || (entry.deferred && !slices.Contains(e.state.AdvertisedToolNames, call.Name)) {
				return fmt.Sprintf("tool batch was not executed: tool %q is not available", call.Name), nil
			}
			_, err = entry.prepare(call, nil)
		}
		if err != nil {
			if _, defect := errors.AsType[*tool.InputValidationPanicError](err); defect {
				return "", errToolInputValidatorDefect
			}
			return fmt.Sprintf("tool batch was not executed: %q has invalid arguments: %s", call.Name, run.NormalizeDiagnostic(err.Error())), nil
		}
	}
	return "", nil
}

func (e *execution) acceptFinalModelResponse(
	ctx context.Context,
	consumedSignals uint32,
	response *chat.Response,
) (run.Transition, error) {
	modelOutput := response.Output
	if modelOutput == nil || modelOutput.Message == nil || modelOutput.FinishReason == "" {
		return run.Transition{}, fmt.Errorf("%w: model response has no finished assistant message", ErrInvalidModelResponse)
	}
	if e.state.PendingSteer == nil {
		return e.finishOrRetry(ctx, consumedSignals, Output{
			ModelResponse: response,
			ModelCalls:    e.state.ModelCallCount,
		}, []chat.Message{modelOutput.Message.Clone()})
	}
	if err := e.state.appendToContext("steered final response", *modelOutput.Message); err != nil {
		return run.Transition{}, err
	}
	appliedSteerSignalIDs, err := e.state.applyPendingSteer()
	if err != nil {
		return run.Transition{}, err
	}
	return e.requestModel(ctx, consumedSignals, appliedSteerSignalIDs)
}

// complete admits an Output by the canonical payload encoding the Runtime
// delivers, so a response too large to deliver becomes a definite failure here
// rather than an opaque encode error that strands the Interaction. finishOrRetry
// already validated the Output, so size is the only encode failure left.
func (e *execution) complete(consumedSignals uint32, output Output) (run.Transition, error) {
	encoded, err := run.EncodePayload(output)
	if err != nil {
		if errors.Is(err, run.ErrPayloadTooLarge) {
			return stepfail.Transition(
				consumedSignals,
				run.FailureKindExecution,
				failureCodeInteractionLimitResponseBytes,
				"final response exceeds the deliverable payload size limit",
			)
		}
		return run.Transition{}, err
	}
	e.state.complete()
	return run.Complete(consumedSignals, encoded)
}

func (e *execution) finishOrRetry(
	ctx context.Context,
	consumedSignals uint32,
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
				consumedSignals,
				run.FailureKindExecution,
				failureCodeInteractionCompletionValidatorFailed,
				err.Error(),
			)
		}
	}
	if !decision.Valid() {
		return stepfail.Transition(
			consumedSignals,
			run.FailureKindContract,
			failureCodeInteractionCompletionDecisionInvalid,
			"CompletionValidator returned an invalid decision",
		)
	}
	if decision.Accepted {
		return e.complete(consumedSignals, output)
	}
	feedback := chat.NewUserMessage(chat.NewTextPart(decision.Feedback))
	if err := e.state.appendToContext("completion retry request", slices.Concat(completionContext, []chat.Message{feedback})...); err != nil {
		return run.Transition{}, err
	}
	e.state.ToolRound = nil
	e.state.PendingSteer = nil
	return e.requestModel(ctx, consumedSignals, nil)
}

func (e *execution) advanceToolCallBatch(ctx context.Context, consumedSignals uint32) (run.Transition, error) {
	calls, err := validatedToolCalls(e.state.ToolRound.Response)
	if err != nil || uint64(len(calls)) > math.MaxUint32 ||
		uint64(e.state.ToolRound.nextCallIndex()) > uint64(len(calls)) {
		return run.Transition{}, fmt.Errorf("%w: invalid pending ToolCall batch", ErrInvalidExecutionState)
	}
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	if e.state.ToolRound.nextCallIndex() == uint32(len(calls)) {
		if err := e.state.ToolRound.validateComplete(ctx); err != nil {
			return run.Transition{}, err
		}
		return run.Checkpoint(consumedSignals)
	}
	call := calls[e.state.ToolRound.nextCallIndex()]
	if e.state.ToolRound.truncated() {
		return e.rejectCall(consumedSignals, call, fmt.Sprintf("tool %q was not executed because model output reached its token limit; emit the complete call again", call.Name))
	}
	if delegate, delegated := e.definition.delegate(call.Name); delegated {
		input, err := delegate.prepareInput(call)
		if err != nil {
			return e.rejectCall(consumedSignals, call, err.Error())
		}
		effects, prepareErr := e.prepareDelegateChildren(ctx, calls, input)
		if prepareErr != nil {
			return run.Transition{}, prepareErr
		}
		return run.Continue(consumedSignals, effects...)
	}
	if _, found := e.definition.tools.entries[call.Name]; !found {
		return e.rejectCall(consumedSignals, call, fmt.Sprintf("tool %q is not available", call.Name))
	}
	return e.startToolChildren(ctx, consumedSignals, calls)
}

// rejectCall answers call with a model-visible rejection and keeps advancing
// the round, so the next Step takes the following call.
func (e *execution) rejectCall(consumedSignals uint32, call chat.ToolCall, reason string) (run.Transition, error) {
	e.state.ToolRound.reject(call, reason)
	return run.Checkpoint(consumedSignals)
}

func (e *execution) finishToolCallBatch(
	ctx context.Context,
	consumedSignals uint32,
	assistant *chat.Message,
) (run.Transition, error) {
	calls, err := validatedToolCalls(e.state.ToolRound.Response)
	if err != nil {
		return run.Transition{}, err
	}
	results := make([]chat.ToolResult, len(e.state.ToolRound.Results))
	direct := e.state.PendingSteer == nil
	for index, result := range e.state.ToolRound.Results {
		results[index] = result.toolResult(calls[index])
		direct = direct && result.Disposition == ResultSucceeded && e.definition.tools.completesDirectly(calls[index].Name)
	}
	completionContext := []chat.Message{assistant.Clone(), chat.NewToolMessage(results...)}
	if err = e.state.recordRoundArtifacts(e.definition); err != nil {
		return run.Transition{}, err
	}
	e.state.ToolRound = nil
	if direct {
		return e.finishOrRetry(ctx, consumedSignals, Output{
			DirectToolResults: results,
			ModelCalls:        e.state.ModelCallCount,
		}, completionContext)
	}
	if err = e.state.appendToContext("continuation request", completionContext...); err != nil {
		return run.Transition{}, err
	}
	appliedSteerSignalIDs, err := e.state.applyPendingSteer()
	if err != nil {
		return run.Transition{}, err
	}
	return e.requestModel(ctx, consumedSignals, appliedSteerSignalIDs)
}

func collectSteerSignals(signals []run.Signal) (steerBatch, uint32, error) {
	var batch steerBatch
	for _, signal := range signals {
		recognized, err := batch.collectSignal(signal)
		if err != nil {
			return steerBatch{}, 0, err
		}
		if !recognized {
			return steerBatch{}, 0, fmt.Errorf("%w: expected external steering input", ErrInvalidExecutionState)
		}
	}
	return batch, uint32(len(signals)), nil
}

// collectModelResult separates the model call's settlement from steering
// input. A failed settlement returns its host diagnostic instead of a result.
func collectEffectResult(signals []run.Signal, expected operation) (signalEnvelope, string, steerBatch, uint32, error) {
	var result signalEnvelope
	var hostFailure string
	var found bool
	var steer steerBatch
	for _, signal := range signals {
		frame, err := signal.Frame[run.SignalFrame]()
		if err != nil {
			return signalEnvelope{}, "", steerBatch{}, 0, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
		switch body := frame.(type) {
		case run.Settlement:
			if found {
				return signalEnvelope{}, "", steerBatch{}, 0, fmt.Errorf("%w: duplicate %q Signal", ErrInvalidExecutionState, expected)
			}
			envelope, diagnostic, err := decodeSettlement(body)
			if err != nil {
				return signalEnvelope{}, "", steerBatch{}, 0, err
			}
			if diagnostic == "" && envelope.operation() != expected {
				return signalEnvelope{}, "", steerBatch{}, 0, fmt.Errorf("%w: got %q while awaiting %q", ErrInvalidExecutionState, envelope.operation(), expected)
			}
			found, result, hostFailure = true, envelope, diagnostic
		case run.ExternalInput:
			if err := steer.collectInput(signal, body); err != nil {
				return signalEnvelope{}, "", steerBatch{}, 0, err
			}
		default:
			return signalEnvelope{}, "", steerBatch{}, 0, fmt.Errorf("%w: unexpected %T while awaiting %q", ErrInvalidExecutionState, frame, expected)
		}
	}
	if !found {
		return signalEnvelope{}, "", steerBatch{}, 0, fmt.Errorf("%w: %q settlement Signal is missing", ErrInvalidExecutionState, expected)
	}
	return result, hostFailure, steer, uint32(len(signals)), nil
}

func (e *execution) acceptChildStarts(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	starts, steer, consumed, err := collectChildStarts(signals)
	if err != nil {
		return run.Transition{}, err
	}
	if steerErr := e.state.addSteer(steer); steerErr != nil {
		return run.Transition{}, steerErr
	}
	calls, err := e.state.ToolRound.activeCalls(ctx)
	if err != nil {
		return run.Transition{}, err
	}
	batch := e.state.ToolRound.ChildBatch
	keys, err := batch.childKeys(e.state.ModelCallCount, e.state.ToolRound.nextCallIndex(), calls)
	if err != nil {
		return run.Transition{}, err
	}
	indices, err := batch.acceptStarts(starts, keys)
	if err != nil {
		return run.Transition{}, err
	}
	for offset, index := range indices {
		if err := ctx.Err(); err != nil {
			return run.Transition{}, err
		}
		failure, failed := starts[offset].Failure()
		if !failed {
			continue
		}
		if batch.Kind == childCallsTool {
			return run.Fail(consumed, failure)
		}
		batch.Invocations[index].Result = rejectedDelegateStartResult(calls[index], failure)
	}
	if batch.childCount() == 0 {
		if err := e.finishChildBatch(); err != nil {
			return run.Transition{}, err
		}
		return e.advanceToolCallBatch(ctx, consumed)
	}
	return e.waitForChildren(ctx, consumed)
}

// activeChildKeys derives the active batch's ChildKeys from the calls it serves.
func (e *execution) activeChildKeys(ctx context.Context) ([]run.ChildKey, error) {
	calls, err := e.state.ToolRound.activeCalls(ctx)
	if err != nil {
		return nil, err
	}
	return e.state.ToolRound.ChildBatch.childKeys(e.state.ModelCallCount, e.state.ToolRound.nextCallIndex(), calls)
}

func (e *execution) waitForChildren(ctx context.Context, consumed uint32) (run.Transition, error) {
	keys, err := e.activeChildKeys(ctx)
	if err != nil {
		return run.Transition{}, err
	}
	spec, err := e.state.ToolRound.ChildBatch.waitSpec(keys)
	if err != nil {
		return run.Transition{}, err
	}
	effect, err := run.NewChildWaitEffect(spec)
	if err != nil {
		return run.Transition{}, err
	}
	return run.Continue(consumed, effect)
}

func (e *execution) acceptChildWaitOpen(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	opened, steer, consumed, err := collectChildWaitOpened(signals)
	if err != nil {
		return run.Transition{}, err
	}
	if steerErr := e.state.addSteer(steer); steerErr != nil {
		return run.Transition{}, steerErr
	}
	keys, err := e.activeChildKeys(ctx)
	if err != nil {
		return run.Transition{}, err
	}
	if err := e.state.ToolRound.ChildBatch.acceptWaitOpened(opened, keys); err != nil {
		return run.Transition{}, err
	}
	return run.Wait(consumed, opened.WaitID())
}

func (e *execution) acceptChildCompletions(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	completed, steer, consumed, err := collectChildWaitSatisfied(signals)
	if err != nil {
		return run.Transition{}, err
	}
	if steerErr := e.state.addSteer(steer); steerErr != nil {
		return run.Transition{}, steerErr
	}
	calls, err := e.state.ToolRound.activeCalls(ctx)
	if err != nil {
		return run.Transition{}, err
	}
	batch := e.state.ToolRound.ChildBatch
	keys, err := batch.childKeys(e.state.ModelCallCount, e.state.ToolRound.nextCallIndex(), calls)
	if err != nil {
		return run.Transition{}, err
	}
	want, err := batch.waitSpec(keys)
	if err != nil {
		return run.Transition{}, err
	}
	indices, err := batch.validateCompletions(completed, want, keys)
	if err != nil {
		return run.Transition{}, err
	}
	failure, failed, err := batch.Kind.terminalFailure(completed.Outcomes())
	if err != nil {
		return run.Transition{}, err
	}
	if failed {
		return run.Fail(consumed, failure)
	}
	for offset, outcome := range completed.Outcomes() {
		if err := ctx.Err(); err != nil {
			return run.Transition{}, err
		}
		index := indices[offset]
		if err := e.acceptChildOutcome(batch.Kind, index, calls[index], outcome.Result()); err != nil {
			return run.Transition{}, err
		}
	}
	batch.WaitID = nil
	if batch.Kind == childCallsTool {
		return e.scheduleToolChildren(ctx, consumed)
	}
	if err := e.finishChildBatch(); err != nil {
		return run.Transition{}, err
	}
	return e.advanceToolCallBatch(ctx, consumed)
}

func (e *execution) acceptChildOutcome(kind childCallKind, index int, call chat.ToolCall, result run.Result[run.Payload]) error {
	if kind == childCallsDelegate {
		return e.acceptDelegateOutcome(index, call, result)
	}
	// terminalFailure already excluded every non-Completed outcome, and a
	// validated Completed termination always carries Output.
	encoded, _ := result.Termination().Output()
	decoded, err := encoded.Decode[toolCallResult]()
	if err != nil {
		return err
	}
	if err := decoded.validateCall(call); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	e.state.ToolRound.ChildBatch.Invocations[index].Result = &decoded
	return nil
}

func (e *execution) finishChildBatch() error {
	names, err := e.state.ToolRound.finishChildren(e.definition.tools, e.state.AdvertisedToolNames)
	if err != nil {
		return err
	}
	e.state.AdvertisedToolNames = names
	return nil
}

// prepareDelegateChildren opens the delegate window at the round cursor. The
// caller already admitted and prepared the first call's input, rejecting it on
// failure, so firstInput is reused here; each following delegate whose input
// prepares extends the window until a non-delegate or an invalid input ends it.
// Every call is prepared exactly once. A cancellation or construction failure
// returns before beginChildren, so no partial batch is committed.
func (e *execution) prepareDelegateChildren(ctx context.Context, calls []chat.ToolCall, firstInput run.Payload) ([]run.Effect, error) {
	start := e.state.ToolRound.nextCallIndex()
	batch := &childCallBatch{Kind: childCallsDelegate}
	var effects []run.Effect
	for index := start; index < uint32(len(calls)); index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		call := calls[index]
		delegate, delegated := e.definition.delegate(call.Name)
		if !delegated {
			break
		}
		input := firstInput
		if index != start {
			prepared, err := delegate.prepareInput(call)
			if err != nil {
				break
			}
			input = prepared
		}
		key, err := DelegateChildKey(e.state.ModelCallCount, call)
		if err != nil {
			return nil, err
		}
		effect, err := run.NewChildStartEffect(run.ChildSpec{
			Key: key, ProgramRef: delegate.deployment.ProgramRef(), Input: input,
			Budget: delegate.budget, Capabilities: delegate.capabilities,
		})
		if err != nil {
			return nil, err
		}
		batch.Invocations = append(batch.Invocations, &childInvocationState{})
		effects = append(effects, effect)
	}
	// The caller admitted calls[start] as a delegate, so the window is non-empty.
	if len(effects) == 0 {
		return nil, fmt.Errorf("%w: delegate window at ToolCall %d is empty", ErrInvalidExecutionState, start)
	}
	e.state.ToolRound.beginChildren(batch)
	return effects, nil
}

func (e *execution) acceptDelegateOutcome(index int, call chat.ToolCall, result run.Result[run.Payload]) error {
	converted, err := delegateToolResult(call, result)
	if err != nil {
		return err
	}
	e.state.ToolRound.ChildBatch.Invocations[index].Result = new(newToolCallResult(converted))
	return nil
}

func (e *execution) startToolChildren(ctx context.Context, consumed uint32, calls []chat.ToolCall) (run.Transition, error) {
	count, err := e.definition.toolWindow(ctx, calls[e.state.ToolRound.nextCallIndex():])
	if err != nil {
		return run.Transition{}, err
	}
	e.state.ToolRound.beginChildren(&childCallBatch{Kind: childCallsTool, Invocations: make([]*childInvocationState, count)})
	return e.scheduleToolChildren(ctx, consumed)
}

func (e *execution) scheduleToolChildren(ctx context.Context, consumed uint32) (run.Transition, error) {
	calls, err := e.state.ToolRound.activeCalls(ctx)
	if err != nil {
		return run.Transition{}, err
	}
	batch := e.state.ToolRound.ChildBatch
	active := e.state.ToolRound.ChildBatch.childCount()
	var effects []run.Effect
	for index := batch.nextStartIndex(); active < e.definition.maxConcurrentToolCalls && index < len(calls); index++ {
		if err := ctx.Err(); err != nil {
			return run.Transition{}, err
		}
		key, keyErr := ToolChildKey(e.state.ModelCallCount, e.state.ToolRound.nextCallIndex()+uint32(index))
		if keyErr != nil {
			return run.Transition{}, keyErr
		}
		input, inputErr := run.EncodePayload(toolCall{Call: calls[index]})
		if inputErr != nil {
			return run.Transition{}, inputErr
		}
		effect, effectErr := run.NewChildStartEffect(run.ChildSpec{
			Key: key, ProgramRef: e.definition.toolDeployment.ProgramRef(), Input: input,
			Budget: e.definition.toolBudget, Capabilities: e.definition.toolCapabilities,
		})
		if effectErr != nil {
			return run.Transition{}, effectErr
		}
		batch.Invocations[index] = &childInvocationState{}
		effects = append(effects, effect)
		active++
	}
	if len(effects) != 0 {
		return run.Continue(consumed, effects...)
	}
	if active != 0 {
		return e.waitForChildren(ctx, consumed)
	}
	if err := e.finishChildBatch(); err != nil {
		return run.Transition{}, err
	}
	return e.advanceToolCallBatch(ctx, consumed)
}

var _ run.Execution = (*execution)(nil)

const (
	failureCodeInteractionCompletionDecisionInvalid  = "interaction.completion.decision_invalid"
	failureCodeInteractionCompletionValidatorFailed  = "interaction.completion.validator_failed"
	failureCodeInteractionDelegateUnresolvedEffects  = "interaction.delegate.unresolved_effects"
	failureCodeInteractionHostFailed                 = "interaction.host.failed"
	failureCodeInteractionLimitModelCalls            = "interaction.limit.model_calls"
	failureCodeInteractionLimitResponseBytes         = "interaction.limit.response_bytes"
	failureCodeInteractionModelToolCallsNotCompleted = "interaction.model.tool_calls_not_completed"
	failureCodeInteractionToolProcessFailed          = "interaction.tool.process_failed"
)
