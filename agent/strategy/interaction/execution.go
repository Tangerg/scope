package interaction

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

type execution struct {
	definition *Definition
	state      executionState
}

// Step advances exactly one pure Interaction boundary. Model and tool I/O are
// represented as dispatcher Effects and therefore never occur in this method.
func (e *execution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	transition, err := e.step(ctx, signals)
	if err == nil {
		return transition, nil
	}
	var kind agent.FailureKind
	var code string
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return agent.Transition{}, err
	case errors.Is(err, ErrInvalidSteer):
		kind, code = agent.FailureKindContract, failureCodeInteractionSignalInvalid
	case errors.Is(err, ErrInvalidProtocol):
		kind, code = agent.FailureKindContract, failureCodeInteractionProtocolInvalid
	case errors.Is(err, ErrInvalidExecutionState):
		kind, code = agent.FailureKindContract, failureCodeInteractionStateInvalid
	default:
		return agent.Transition{}, err
	}
	failure, failureErr := agent.NewFailure(kind, code, agent.NormalizeDiagnostic(err.Error()))
	if failureErr != nil {
		return agent.Transition{}, failureErr
	}
	return agent.Transition{}, &agent.StepError{Failure: failure, Cause: err}
}

func (e *execution) step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if e == nil || !e.definition.valid() {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch e.state.Phase {
	case phaseReadyModel:
		steer, consumedSignals, err := collectSteerSignals(signals)
		if err != nil {
			return agent.Transition{}, err
		}
		if addSteerErr := e.addSteer(steer); addSteerErr != nil {
			return agent.Transition{}, addSteerErr
		}
		appliedSteerSignalIDs, err := e.applyPendingSteer()
		if err != nil {
			return agent.Transition{}, err
		}
		return e.requestModel(consumedSignals, appliedSteerSignalIDs)
	case phaseAdvancingTools, phaseRoundComplete:
		steer, consumed, err := collectSteerSignals(signals)
		if err != nil {
			return agent.Transition{}, err
		}
		if steerErr := e.addSteer(steer); steerErr != nil {
			return agent.Transition{}, steerErr
		}
		if e.state.Phase == phaseRoundComplete {
			return e.finishToolCallBatch(consumed, e.state.ToolRound.Response.Output.Message)
		}
		return e.advanceToolCallBatch(ctx, consumed)
	case phaseAwaitingModel:
		return e.acceptModel(ctx, signals)
	case phaseAwaitingChildStarts:
		return e.acceptChildStarts(ctx, signals)
	case phaseAwaitingChildWaitOpen:
		return e.acceptChildWaitOpen(signals)
	case phaseWaitingChildren:
		return e.acceptChildCompletions(ctx, signals)
	case phaseCompleted:
		return agent.Transition{}, fmt.Errorf("%w: completed execution cannot advance", ErrInvalidExecutionState)
	default:
		return agent.Transition{}, ErrInvalidExecutionState
	}
}

// Snapshot returns a complete, self-sufficient WorkingContext and checkpoint.
// Restore owns full validation, including Engine admission of each candidate.
func (e *execution) Snapshot() (agent.ExecutionState, error) {
	return e.state.snapshot()
}

func (e *execution) requestModel(
	consumedSignals uint32,
	appliedSteerSignalIDs []agent.SignalID,
) (agent.Transition, error) {
	if !e.definition.maxModelCalls.Allows(e.state.ModelCallCount, 1) {
		return e.fail(
			consumedSignals,
			agent.FailureKindExecution,
			failureCodeInteractionLimitModelCalls,
			"Interaction reached its configured model-call limit before a final response",
		)
	}
	if e.state.ModelCallCount == ^uint64(0) {
		return agent.Transition{}, agent.ErrCounterExhausted
	}
	modelCallSequence := e.state.ModelCallCount + 1
	envelope, err := newModelEffect(
		e.state.WorkingContext,
		modelCallSequence,
		e.state.AdvertisedToolNames,
		appliedSteerSignalIDs,
	)
	if err != nil {
		return agent.Transition{}, err
	}
	payload, err := jsonv2.Marshal(envelope, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewDispatcherEffect(payload)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.ModelCallCount = modelCallSequence
	e.state.Phase = phaseAwaitingModel
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) acceptModel(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	envelope, steer, consumedSignals, err := collectExpectedSignal(signals, operationModelCall)
	if err != nil {
		return agent.Transition{}, err
	}
	if envelope.ModelResult.HostError != "" {
		return e.fail(
			consumedSignals,
			agent.FailureKindExternal,
			failureCodeInteractionHostFailed,
			envelope.ModelResult.HostError,
		)
	}
	if envelope.ModelResult.Error != "" {
		return e.fail(
			consumedSignals,
			agent.FailureKindExternal,
			failureCodeInteractionModelFailed,
			envelope.ModelResult.Error,
		)
	}
	if replacement := envelope.ModelResult.ReplacementMessages; replacement != nil {
		effective := e.state.WorkingContext.Clone()
		effective.Messages = cloneMessages(replacement)
		if effectiveErr := effective.Validate(); effectiveErr != nil {
			return agent.Transition{}, fmt.Errorf("%w: replacement model context: %w", ErrInvalidExecutionState, effectiveErr)
		}
		e.state.WorkingContext = effective
	}
	response := envelope.ModelResult.Response.Clone()
	calls, err := validatedToolCalls(response)
	if err != nil {
		failure, failureErr := agent.NewFailure(agent.FailureKindExternal, failureCodeInteractionModelInvalidResponse, agent.NormalizeDiagnostic(err.Error()))
		if failureErr != nil {
			return agent.Transition{}, failureErr
		}
		return agent.Transition{}, &agent.StepError{Failure: failure, Cause: err}
	}
	if len(calls) > 0 && response.Output.FinishReason != chat.FinishReasonToolCalls &&
		response.Output.FinishReason != chat.FinishReasonLength {
		return e.fail(
			consumedSignals,
			agent.FailureKindExternal,
			failureCodeInteractionModelToolCallsNotCompleted,
			fmt.Sprintf("model output ended with %q; tool calls were not executed", response.Output.FinishReason),
		)
	}
	if addSteerErr := e.addSteer(steer); addSteerErr != nil {
		return agent.Transition{}, addSteerErr
	}
	if len(calls) == 0 {
		return e.acceptFinalModelResponse(consumedSignals, response)
	}
	e.state.ToolRound = &toolCallRound{Response: response}
	e.state.Phase = phaseAdvancingTools
	return agent.Checkpoint(consumedSignals)
}

func (e *execution) acceptFinalModelResponse(
	consumedSignals uint32,
	response *chat.Response,
) (agent.Transition, error) {
	modelOutput := response.Output
	if modelOutput == nil || modelOutput.Message == nil || modelOutput.FinishReason == "" {
		failure, err := agent.NewFailure(agent.FailureKindExternal, failureCodeInteractionModelInvalidResponse, "model response has no finished assistant message")
		if err != nil {
			return agent.Transition{}, err
		}
		return agent.Transition{}, &agent.StepError{Failure: failure}
	}
	if e.state.PendingSteer == nil {
		return e.finishOrRetry(consumedSignals, Output{
			Source:        CompletionSourceModelResponse,
			ModelResponse: response,
			ModelCalls:    e.state.ModelCallCount,
		}, []chat.Message{modelOutput.Message.Clone()})
	}
	request := e.state.WorkingContext.Clone()
	request.Messages = append(request.Messages, modelOutput.Message.Clone())
	e.state.WorkingContext = request
	appliedSteerSignalIDs, err := e.applyPendingSteer()
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseReadyModel
	return e.requestModel(consumedSignals, appliedSteerSignalIDs)
}

func (e *execution) complete(consumedSignals uint32, output Output) (agent.Transition, error) {
	if err := output.Validate(); err != nil {
		return agent.Transition{}, err
	}
	encoded, err := agent.EncodePayload(output)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.complete(output)
	return agent.Complete(consumedSignals, encoded)
}

func (e *execution) finishOrRetry(
	consumedSignals uint32,
	output Output,
	completionContext []chat.Message,
) (agent.Transition, error) {
	if err := output.Validate(); err != nil {
		return agent.Transition{}, err
	}
	decision := CompletionDecision{Accepted: true}
	if e.definition.completionValidator != nil {
		candidate := CompletionCandidate{
			workingContext: e.state.WorkingContext.Clone(),
			output:         output.clone(),
			artifacts:      newArtifacts(e.state.ArtifactRecords),
		}
		var err error
		decision, err = e.definition.completionValidator(candidate)
		if err != nil {
			return e.fail(
				consumedSignals,
				agent.FailureKindExecution,
				failureCodeInteractionCompletionValidatorFailed,
				err.Error(),
			)
		}
	}
	if !decision.Valid() {
		return e.fail(
			consumedSignals,
			agent.FailureKindContract,
			failureCodeInteractionCompletionDecisionInvalid,
			"CompletionValidator returned an invalid decision",
		)
	}
	if decision.Accepted {
		return e.complete(consumedSignals, output)
	}
	request := e.state.WorkingContext.Clone()
	request.Messages = append(request.Messages, cloneMessages(completionContext)...)
	request.Messages = append(request.Messages, chat.NewUserMessage(chat.NewTextPart(decision.Feedback)))
	if err := request.Validate(); err != nil {
		return agent.Transition{}, fmt.Errorf("%w: completion retry request: %w", ErrInvalidExecutionState, err)
	}
	e.state.WorkingContext = request
	e.state.ToolRound = nil
	e.state.PendingSteer = nil
	e.state.Phase = phaseReadyModel
	return e.requestModel(consumedSignals, nil)
}

func (e *execution) advanceToolCallBatch(ctx context.Context, consumedSignals uint32) (agent.Transition, error) {
	calls, err := validatedToolCalls(e.state.ToolRound.Response)
	if err != nil || uint64(len(calls)) > uint64(^uint32(0)) ||
		uint64(e.state.ToolRound.nextCallIndex()) > uint64(len(calls)) {
		return agent.Transition{}, fmt.Errorf("%w: invalid pending ToolCall batch", ErrInvalidExecutionState)
	}
	for {
		if err := ctx.Err(); err != nil {
			return agent.Transition{}, err
		}
		if e.state.ToolRound.nextCallIndex() == uint32(len(calls)) {
			if err := e.state.ToolRound.validateComplete(ctx); err != nil {
				return agent.Transition{}, err
			}
			e.state.Phase = phaseRoundComplete
			return agent.Checkpoint(consumedSignals)
		}
		call := calls[e.state.ToolRound.nextCallIndex()]
		if e.state.ToolRound.Response.Output.FinishReason == chat.FinishReasonLength {
			e.state.ToolRound.reject(call, fmt.Sprintf("tool %q was not executed because model output reached its token limit; emit the complete call again", call.Name))
			e.state.Phase = phaseAdvancingTools
			return agent.Checkpoint(consumedSignals)
		}
		if delegate, delegated := e.definition.delegate(call.Name); delegated {
			if _, err := delegate.prepareInput(call); err != nil {
				e.state.ToolRound.reject(call, err.Error())
				e.state.Phase = phaseAdvancingTools
				return agent.Checkpoint(consumedSignals)
			}
			effects, prepareErr := e.prepareDelegateChildren(ctx, calls)
			if prepareErr != nil {
				return agent.Transition{}, prepareErr
			}
			if len(effects) != 0 {
				e.state.Phase = phaseAwaitingChildStarts
				return agent.Continue(consumedSignals, effects...)
			}
			if finishErr := e.finishChildBatch(); finishErr != nil {
				return agent.Transition{}, finishErr
			}
			continue
		}
		if _, found := e.definition.tools.entries[call.Name]; !found {
			e.state.ToolRound.reject(call, fmt.Sprintf("tool %q is not available", call.Name))
			e.state.Phase = phaseAdvancingTools
			return agent.Checkpoint(consumedSignals)
		}
		return e.startToolChildren(ctx, consumedSignals, calls)
	}
}

func (e *execution) finishToolCallBatch(
	consumedSignals uint32,
	assistant *chat.Message,
) (agent.Transition, error) {
	results := make([]chat.ToolResult, len(e.state.ToolRound.Results))
	direct := e.state.PendingSteer == nil
	for index, result := range e.state.ToolRound.Results {
		results[index] = result.Result.Clone()
		direct = direct && result.Direct
	}
	completionContext := []chat.Message{assistant.Clone(), chat.NewToolMessage(results...)}
	e.state.ToolRound = nil
	e.state.Phase = phaseReadyModel
	if direct {
		return e.finishOrRetry(consumedSignals, Output{
			Source:            CompletionSourceDirectToolResults,
			DirectToolResults: results,
			ModelCalls:        e.state.ModelCallCount,
		}, completionContext)
	}
	request := e.state.WorkingContext.Clone()
	request.Messages = append(request.Messages, completionContext...)
	e.state.WorkingContext = request
	appliedSteerSignalIDs, err := e.applyPendingSteer()
	if err != nil {
		return agent.Transition{}, err
	}
	if err := e.state.WorkingContext.Validate(); err != nil {
		return agent.Transition{}, fmt.Errorf("%w: continuation request: %w", ErrInvalidExecutionState, err)
	}
	return e.requestModel(consumedSignals, appliedSteerSignalIDs)
}

func (e *execution) addSteer(batch steerBatch) error {
	if batch.empty() {
		return nil
	}
	if err := batch.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if e.state.PendingSteer == nil {
		cloned := batch.clone()
		e.state.PendingSteer = &cloned
		return nil
	}
	e.state.PendingSteer.Messages = append(
		e.state.PendingSteer.Messages,
		cloneMessages(batch.Messages)...,
	)
	e.state.PendingSteer.SignalIDs = append(
		e.state.PendingSteer.SignalIDs,
		batch.SignalIDs...,
	)
	if err := e.state.PendingSteer.validate(); err != nil {
		return fmt.Errorf("%w: merged pending steer: %w", ErrInvalidExecutionState, err)
	}
	return nil
}

func (e *execution) applyPendingSteer() ([]agent.SignalID, error) {
	if e.state.PendingSteer == nil {
		return nil, nil
	}
	if err := e.state.PendingSteer.validate(); err != nil {
		return nil, fmt.Errorf("%w: pending steer: %w", ErrInvalidExecutionState, err)
	}
	request := e.state.WorkingContext.Clone()
	request.Messages = append(request.Messages, cloneMessages(e.state.PendingSteer.Messages)...)
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("%w: steered model request: %w", ErrInvalidExecutionState, err)
	}
	appliedSignalIDs := slices.Clone(e.state.PendingSteer.SignalIDs)
	e.state.WorkingContext = request
	e.state.PendingSteer = nil
	return appliedSignalIDs, nil
}

func collectSteerSignals(signals []agent.Signal) (steerBatch, uint32, error) {
	var batch steerBatch
	for _, signal := range signals {
		envelope, err := decodeSignal(signal.Payload())
		if err != nil {
			return steerBatch{}, 0, err
		}
		if envelope.Operation != operationSteer {
			return steerBatch{}, 0, fmt.Errorf("%w: unexpected %q Signal", ErrInvalidExecutionState, envelope.Operation)
		}
		if err := batch.appendSignal(signal, envelope.Steer.Messages); err != nil {
			return steerBatch{}, 0, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
	}
	return batch, uint32(len(signals)), nil
}

func collectExpectedSignal(
	signals []agent.Signal,
	expected operation,
) (signalEnvelope, steerBatch, uint32, error) {
	var result signalEnvelope
	var found bool
	var steer steerBatch
	for _, signal := range signals {
		envelope, err := decodeSignal(signal.Payload())
		if err != nil {
			return signalEnvelope{}, steerBatch{}, 0, err
		}
		switch envelope.Operation {
		case operationSteer:
			if err := steer.appendSignal(signal, envelope.Steer.Messages); err != nil {
				return signalEnvelope{}, steerBatch{}, 0, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
			}
		case expected:
			if !signal.EngineOwned() {
				return signalEnvelope{}, steerBatch{}, 0, fmt.Errorf("%w: %q Signal requires Engine authority", ErrInvalidExecutionState, expected)
			}
			if found {
				return signalEnvelope{}, steerBatch{}, 0, fmt.Errorf("%w: duplicate %q Signal", ErrInvalidExecutionState, expected)
			}
			_, addressed := signal.WaitID()
			requiresAddress := expected == operationWaitOpened || expected == operationInputResponse
			if addressed != requiresAddress {
				return signalEnvelope{}, steerBatch{}, 0, fmt.Errorf("%w: %q Signal has invalid wait addressing", ErrInvalidExecutionState, expected)
			}
			found = true
			result = envelope
		default:
			return signalEnvelope{}, steerBatch{}, 0, fmt.Errorf("%w: got %q while awaiting %q", ErrInvalidExecutionState, envelope.Operation, expected)
		}
	}
	if !found {
		return signalEnvelope{}, steerBatch{}, 0, fmt.Errorf("%w: %q settlement Signal is missing", ErrInvalidExecutionState, expected)
	}
	return result, steer, uint32(len(signals)), nil
}

func (e *execution) fail(
	consumedSignals uint32,
	kind agent.FailureKind,
	code string,
	message string,
) (agent.Transition, error) {
	message = agent.NormalizeDiagnostic(message)
	failure, err := agent.NewFailure(kind, code, message)
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Fail(consumedSignals, failure)
}

func (e *execution) childBindings(calls []chat.ToolCall) ([]agent.DeploymentRef, error) {
	bindings := make([]agent.DeploymentRef, len(calls))
	for index, call := range calls {
		if e.state.ToolRound.ChildBatch.Kind == childCallsTool {
			bindings[index] = e.definition.tools.deploymentRef
			continue
		}
		delegate, found := e.definition.delegate(call.Name)
		if !found {
			return nil, ErrInvalidExecutionState
		}
		bindings[index] = delegate.deploymentRef
	}
	return bindings, nil
}

func (e *execution) acceptChildStarts(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	starts, steer, consumed, err := collectChildStarts(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if steerErr := e.addSteer(steer); steerErr != nil {
		return agent.Transition{}, steerErr
	}
	calls, err := e.state.ToolRound.activeCalls(ctx)
	if err != nil {
		return agent.Transition{}, err
	}
	bindings, err := e.childBindings(calls)
	if err != nil {
		return agent.Transition{}, err
	}
	batch := e.state.ToolRound.ChildBatch
	indices, err := batch.acceptStarts(starts, bindings)
	if err != nil {
		return agent.Transition{}, err
	}
	for offset, index := range indices {
		if err := ctx.Err(); err != nil {
			return agent.Transition{}, err
		}
		failure, failed := starts[offset].Failure()
		if !failed {
			continue
		}
		if batch.Kind == childCallsTool {
			return agent.Fail(consumed, failure)
		}
		result := delegateErrorResult(calls[index], "child start failed: "+failure.Code()+": "+failure.Message())
		batch.Invocations[index].Result = &toolCallResult{Result: result, Rejected: true}
	}
	if len(batch.children()) == 0 {
		if err := e.finishChildBatch(); err != nil {
			return agent.Transition{}, err
		}
		return e.advanceToolCallBatch(ctx, consumed)
	}
	return e.waitForChildren(consumed)
}

func (e *execution) waitForChildren(consumed uint32) (agent.Transition, error) {
	spec, err := e.state.ToolRound.ChildBatch.waitSpec(e.state.ModelCallCount, e.state.ToolRound.nextCallIndex())
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewChildWaitEffect(spec)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseAwaitingChildWaitOpen
	return agent.Continue(consumed, effect)
}

func (e *execution) acceptChildWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	opened, steer, consumed, err := collectChildWaitOpened(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if steerErr := e.addSteer(steer); steerErr != nil {
		return agent.Transition{}, steerErr
	}
	want, err := e.state.ToolRound.ChildBatch.waitSpec(e.state.ModelCallCount, e.state.ToolRound.nextCallIndex())
	if err != nil {
		return agent.Transition{}, err
	}
	if err := e.state.ToolRound.ChildBatch.acceptWaitOpened(opened, want); err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseWaitingChildren
	return agent.Wait(consumed, opened.WaitID())
}

func (e *execution) acceptChildCompletions(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	completed, steer, consumed, err := collectChildWaitSatisfied(signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if steerErr := e.addSteer(steer); steerErr != nil {
		return agent.Transition{}, steerErr
	}
	calls, err := e.state.ToolRound.activeCalls(ctx)
	if err != nil {
		return agent.Transition{}, err
	}
	batch := e.state.ToolRound.ChildBatch
	want, err := batch.waitSpec(e.state.ModelCallCount, e.state.ToolRound.nextCallIndex())
	if err != nil {
		return agent.Transition{}, err
	}
	indices, err := batch.validateCompletions(completed, want)
	if err != nil {
		return agent.Transition{}, err
	}
	for _, outcome := range completed.Outcomes() {
		result := outcome.Result()
		if batch.Kind == childCallsDelegate {
			if unresolved, known := outcome.SubtreeUnresolvedEffects(); !known || len(unresolved) > 0 {
				return e.fail(consumed, agent.FailureKindExternal, failureCodeInteractionDelegateUnresolvedEffects, fmt.Sprintf("Delegate subtree %s ended with unresolved Effects %v", result.ProcessID(), unresolved))
			}
		} else if result.Status() != agent.StatusCompleted {
			termination := result.Termination()
			if failure, failed := termination.Failure(); failed {
				return agent.Fail(consumed, failure)
			}
			diagnostic := fmt.Sprintf("Tool child %s ended with %s (%s): %s", result.ProcessID(), result.Status(), termination.Cause(), termination.Reason())
			return e.fail(consumed, agent.FailureKindExecution, failureCodeInteractionToolProcessFailed, diagnostic)
		}
	}
	for offset, outcome := range completed.Outcomes() {
		if err := ctx.Err(); err != nil {
			return agent.Transition{}, err
		}
		index := indices[offset]
		result := outcome.Result()
		if batch.Kind == childCallsDelegate {
			if err := e.acceptDelegateOutcome(index, calls[index], result); err != nil {
				return agent.Transition{}, err
			}
			continue
		}
		encoded, present := result.Output()
		if !present {
			return agent.Transition{}, ErrInvalidExecutionState
		}
		decoded, err := encoded.Decode[toolCallResult]()
		if err != nil {
			return agent.Transition{}, err
		}
		if err := decoded.validateCall(calls[index]); err != nil {
			return agent.Transition{}, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
		batch.Invocations[index].Result = &decoded
	}
	batch.WaitID = nil
	if batch.Kind == childCallsTool {
		return e.scheduleToolChildren(ctx, consumed)
	}
	if err := e.finishChildBatch(); err != nil {
		return agent.Transition{}, err
	}
	return e.advanceToolCallBatch(ctx, consumed)
}

func (e *execution) finishChildBatch() error {
	names, err := e.state.ToolRound.finishChildren(e.definition.tools, e.state.AdvertisedToolNames)
	if err != nil {
		return err
	}
	e.state.AdvertisedToolNames = names
	return nil
}

func (e *execution) prepareDelegateChildren(ctx context.Context, calls []chat.ToolCall) ([]agent.Effect, error) {
	start := e.state.ToolRound.nextCallIndex()
	end := start
	for end < uint32(len(calls)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		delegate, delegated := e.definition.delegate(calls[end].Name)
		if !delegated {
			break
		}
		if _, err := delegate.prepareInput(calls[end]); err != nil {
			break
		}
		end++
	}
	batch := &childCallBatch{Kind: childCallsDelegate,
		Invocations: make([]childInvocationState, end-start), NextStartIndex: end - start}
	effects := make([]agent.Effect, 0, len(batch.Invocations))
	for index := range batch.Invocations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		call := calls[start+uint32(index)]
		delegate, _ := e.definition.delegate(call.Name)
		input, err := delegate.prepareInput(call)
		if err != nil {
			return nil, err
		}
		key, err := DelegateChildKey(e.state.ModelCallCount, call)
		if err != nil {
			return nil, err
		}
		effect, err := agent.NewChildStartEffect(agent.ChildSpec{
			Key: key, DeploymentRef: delegate.deploymentRef, Input: input,
			Budget: delegate.budget, Capabilities: delegate.capabilities,
		})
		if err != nil {
			return nil, err
		}
		batch.Invocations[index].ChildKey = &key
		effects = append(effects, effect)
	}
	e.state.ToolRound.beginChildren(batch)
	return effects, nil
}

func (e *execution) acceptDelegateOutcome(index int, call chat.ToolCall, result agent.Result) error {
	converted, err := delegateToolResult(call, result)
	if err != nil {
		return err
	}
	e.state.ToolRound.ChildBatch.Invocations[index].Result = &toolCallResult{Result: converted}
	if result.Status() != agent.StatusCompleted {
		return nil
	}
	output, present := result.Output()
	delegate, found := e.definition.delegate(call.Name)
	if !present || !found || delegate.outputSchema.Validate(output.JSON()) != nil {
		return fmt.Errorf("%w: Delegate child output violates its frozen contract", ErrInvalidExecutionState)
	}
	e.state.ArtifactRecords = append(e.state.ArtifactRecords, artifactRecord{
		ModelCallSequence: e.state.ModelCallCount,
		ToolCallIndex:     e.state.ToolRound.nextCallIndex() + uint32(index),
		ToolCallID:        call.ID, DelegateName: call.Name, Output: output,
	})
	return nil
}

func (e *execution) startToolChildren(ctx context.Context, consumed uint32, calls []chat.ToolCall) (agent.Transition, error) {
	start := e.state.ToolRound.nextCallIndex()
	count := 1
	if e.definition.maxConcurrentToolCalls > 1 {
		var err error
		count, err = e.definition.tools.concurrentBatchEnd(ctx, calls[start:])
		if err != nil {
			return agent.Transition{}, err
		}
	}
	e.state.ToolRound.beginChildren(&childCallBatch{Kind: childCallsTool, Invocations: make([]childInvocationState, count)})
	return e.scheduleToolChildren(ctx, consumed)
}

func (e *execution) scheduleToolChildren(ctx context.Context, consumed uint32) (agent.Transition, error) {
	calls, err := e.state.ToolRound.activeCalls(ctx)
	if err != nil {
		return agent.Transition{}, err
	}
	batch := e.state.ToolRound.ChildBatch
	active := len(e.state.ToolRound.ChildBatch.children())
	var effects []agent.Effect
	for active < e.definition.maxConcurrentToolCalls && int(batch.NextStartIndex) < len(calls) {
		if err := ctx.Err(); err != nil {
			return agent.Transition{}, err
		}
		index := batch.NextStartIndex
		call := calls[index]
		key, keyErr := toolChildKey(e.state.ModelCallCount, call)
		if keyErr != nil {
			return agent.Transition{}, keyErr
		}
		input, inputErr := agent.EncodePayload(toolCall{
			ModelCallSequence: e.state.ModelCallCount, ToolCallIndex: e.state.ToolRound.nextCallIndex() + index, Call: call,
		})
		if inputErr != nil {
			return agent.Transition{}, inputErr
		}
		effect, effectErr := agent.NewChildStartEffect(agent.ChildSpec{
			Key: key, DeploymentRef: e.definition.tools.deploymentRef, Input: input,
			Budget: e.definition.toolBudget, Capabilities: e.definition.toolCapabilities,
		})
		if effectErr != nil {
			return agent.Transition{}, effectErr
		}
		batch.Invocations[index].ChildKey = &key
		batch.NextStartIndex++
		effects = append(effects, effect)
		active++
	}
	if len(effects) != 0 {
		e.state.Phase = phaseAwaitingChildStarts
		return agent.Continue(consumed, effects...)
	}
	if active != 0 {
		return e.waitForChildren(consumed)
	}
	if err := e.finishChildBatch(); err != nil {
		return agent.Transition{}, err
	}
	return e.advanceToolCallBatch(ctx, consumed)
}

var _ agent.Execution = (*execution)(nil)

const (
	failureCodeInteractionSignalInvalid              = "interaction.signal.invalid"
	failureCodeInteractionProtocolInvalid            = "interaction.protocol.invalid"
	failureCodeInteractionStateInvalid               = "interaction.state.invalid"
	failureCodeInteractionCompletionDecisionInvalid  = "interaction.completion.decision_invalid"
	failureCodeInteractionCompletionValidatorFailed  = "interaction.completion.validator_failed"
	failureCodeInteractionDelegateUnresolvedEffects  = "interaction.delegate.unresolved_effects"
	failureCodeInteractionHostFailed                 = "interaction.host.failed"
	failureCodeInteractionLimitModelCalls            = "interaction.limit.model_calls"
	failureCodeInteractionModelInvalidResponse       = "interaction.model.invalid_response"
	failureCodeInteractionModelFailed                = "interaction.model.failed"
	failureCodeInteractionModelToolCallsNotCompleted = "interaction.model.tool_calls_not_completed"
	failureCodeInteractionToolProcessFailed          = "interaction.tool.process_failed"
)
