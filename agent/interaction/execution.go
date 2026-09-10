package interaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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
	if e == nil || !e.definition.valid() {
		return agent.Transition{}, ErrInvalidExecutionState
	}
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	if err := e.state.Validate(e.definition); err != nil {
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
func (e *execution) Snapshot() (agent.ExecutionState, error) {
	if e == nil || !e.definition.valid() {
		return agent.ExecutionState{}, ErrInvalidExecutionState
	}
	if err := e.state.Validate(e.definition); err != nil {
		return agent.ExecutionState{}, err
	}
	return encodeState(e.state)
}

func (e *execution) requestModel(
	consumedSignals uint32,
	appliedSteerSignalIDs []agent.SignalID,
) (agent.Transition, error) {
	if e.state.ModelCallCount >= e.definition.maxModelCalls {
		return e.fail(
			consumedSignals,
			agent.FailureKindExecution,
			"interaction.limit.model_calls",
			"Interaction reached its configured model-call limit before a final response",
		)
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
	payload, err := encodeProtocol(envelope)
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
			"interaction.host.failed",
			envelope.ModelResult.HostError,
		)
	}
	if envelope.ModelResult.Error != "" {
		return e.fail(
			consumedSignals,
			agent.FailureKindExternal,
			"interaction.model.failed",
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
	calls, _, err := responseToolCalls(response)
	if err != nil {
		return agent.Transition{}, err
	}
	if len(calls) > 0 && response.Output.FinishReason != chat.FinishReasonToolCalls &&
		response.Output.FinishReason != chat.FinishReasonLength {
		return e.fail(
			consumedSignals,
			agent.FailureKindExternal,
			"interaction.model.tool_calls_not_completed",
			fmt.Sprintf("model output ended with %q; tool calls were not executed", response.Output.FinishReason),
		)
	}
	if addSteerErr := e.addSteer(steer); addSteerErr != nil {
		return agent.Transition{}, addSteerErr
	}
	if len(calls) == 0 {
		return e.acceptFinalModelResponse(consumedSignals, response)
	}
	if response.Output.FinishReason == chat.FinishReasonLength {
		return e.rejectTruncatedToolCalls(ctx, consumedSignals, response, calls)
	}

	e.state.ToolRound = &toolCallRound{Response: response, DirectResultEligible: true}
	return e.advanceToolCallBatch(ctx, consumedSignals)
}

func (e *execution) acceptFinalModelResponse(
	consumedSignals uint32,
	response *chat.Response,
) (agent.Transition, error) {
	modelOutput := response.Output
	if modelOutput == nil || modelOutput.Message == nil || modelOutput.FinishReason == "" {
		return agent.Transition{}, fmt.Errorf(
			"%w: final response has no finished assistant message",
			ErrInvalidExecutionState,
		)
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

func (e *execution) rejectTruncatedToolCalls(
	ctx context.Context,
	consumedSignals uint32,
	response *chat.Response,
	calls []chat.ToolCall,
) (agent.Transition, error) {
	if response == nil || response.Output == nil || response.Output.Message == nil || len(calls) == 0 {
		return agent.Transition{}, fmt.Errorf("%w: invalid truncated ToolCall response", ErrInvalidExecutionState)
	}
	results := make([]chat.ToolResult, len(calls))
	for index := range calls {
		if err := ctx.Err(); err != nil {
			return agent.Transition{}, err
		}
		call := calls[index]
		results[index] = chat.ToolResult{
			ID: call.ID, Name: call.Name, IsError: true,
			Output: chat.NewTextToolOutput(fmt.Sprintf(
				"error: tool %q was not executed because model output reached its token limit; emit the complete call again",
				call.Name,
			)),
		}
	}
	request := e.state.WorkingContext.Clone()
	request.Messages = append(
		request.Messages,
		response.Output.Message.Clone(),
		chat.NewToolMessage(results...),
	)
	e.state.WorkingContext = request
	appliedSteerSignalIDs, err := e.applyPendingSteer()
	if err != nil {
		return agent.Transition{}, err
	}
	if err := e.state.WorkingContext.Validate(); err != nil {
		return agent.Transition{}, fmt.Errorf("%w: truncated ToolCall continuation: %w", ErrInvalidExecutionState, err)
	}
	e.state.Phase = phaseReadyModel
	return e.requestModel(consumedSignals, appliedSteerSignalIDs)
}

func (e *execution) complete(consumedSignals uint32, output Output) (agent.Transition, error) {
	if err := output.Validate(); err != nil {
		return agent.Transition{}, err
	}
	encoded, err := agent.EncodeOutput(output)
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
	candidate := CompletionCandidate{
		workingContext: e.state.WorkingContext.Clone(),
		output:         output.clone(),
		artifacts:      newArtifacts(e.state.ArtifactRecords),
	}
	decision := CompletionDecision{Accepted: true}
	if e.definition.completionValidator != nil {
		var err error
		decision, err = e.definition.completionValidator(candidate)
		if err != nil {
			return e.fail(
				consumedSignals,
				agent.FailureKindExecution,
				"interaction.completion.validator_failed",
				err.Error(),
			)
		}
	}
	if !decision.Valid() {
		return e.fail(
			consumedSignals,
			agent.FailureKindContract,
			"interaction.completion.decision_invalid",
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
	for {
		if err := ctx.Err(); err != nil {
			return agent.Transition{}, err
		}
		calls, assistant, err := responseToolCalls(e.state.ToolRound.Response)
		if err != nil || uint64(len(calls)) > uint64(^uint32(0)) ||
			uint64(e.state.ToolRound.nextCallIndex()) > uint64(len(calls)) {
			return agent.Transition{}, fmt.Errorf("%w: invalid pending ToolCall batch", ErrInvalidExecutionState)
		}
		if e.state.ToolRound.nextCallIndex() == uint32(len(calls)) {
			return e.finishToolCallBatch(consumedSignals, assistant)
		}
		if _, delegated := e.definition.delegate(calls[e.state.ToolRound.nextCallIndex()].Name); delegated {
			transition, started, startErr := e.startDelegateChildren(ctx, consumedSignals, calls)
			if startErr != nil {
				return agent.Transition{}, startErr
			}
			if started {
				return transition, nil
			}
			continue
		}
		transition, started, err := e.startToolChildren(ctx, consumedSignals, calls)
		if err != nil {
			return agent.Transition{}, err
		}
		if started {
			return transition, nil
		}
	}
}

func (e *execution) finishToolCallBatch(
	consumedSignals uint32,
	assistant *chat.Message,
) (agent.Transition, error) {
	results := cloneToolResults(e.state.ToolRound.Results)
	completionContext := []chat.Message{assistant.Clone(), chat.NewToolMessage(results...)}
	direct := e.state.ToolRound.DirectResultEligible && e.state.PendingSteer == nil
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

func checkpointWaitKey(modelCallCount uint32, toolCallID string, pauseCount uint32) (agent.WaitKey, error) {
	hash := sha256.New()
	hash.Write([]byte(strconv.FormatUint(uint64(modelCallCount), 10)))
	hash.Write([]byte{0})
	hash.Write([]byte(toolCallID))
	hash.Write([]byte{0})
	hash.Write([]byte(strconv.FormatUint(uint64(pauseCount), 10)))
	return agent.ParseWaitKey("interaction.input." + hex.EncodeToString(hash.Sum(nil)))
}

func sameInputRequest(left, right ToolInputRequest) bool {
	return string(left.Prompt()) == string(right.Prompt()) &&
		string(left.ResponseSchema()) == string(right.ResponseSchema()) &&
		string(left.ContinuationState()) == string(right.ContinuationState())
}

func (e *execution) fail(
	consumedSignals uint32,
	kind agent.FailureKind,
	code string,
	message string,
) (agent.Transition, error) {
	message = boundedDiagnostic(message)
	failure, err := agent.NewFailure(kind, code, message)
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Fail(consumedSignals, failure)
}

func boundedDiagnostic(message string) string {
	message = strings.ToValidUTF8(message, "\ufffd")
	message = strings.TrimSpace(message)
	if message == "" {
		return "Interaction operation failed"
	}
	const limit = 2048
	if len(message) <= limit {
		return message
	}
	message = message[:limit]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "Interaction operation failed"
	}
	return message
}

var _ agent.Execution = (*execution)(nil)
