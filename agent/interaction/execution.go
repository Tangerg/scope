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
func (e *execution) Step(_ context.Context, signals []agent.Signal) (agent.Transition, error) {
	if e == nil || !e.definition.valid() {
		return agent.Transition{}, ErrInvalidExecutionState
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
		return e.acceptModel(signals)
	case phaseAwaitingTools:
		return e.acceptTools(signals)
	case phaseAwaitingInputWaitOpen:
		return e.acceptInputWaitOpen(signals)
	case phaseWaitingInput:
		return e.acceptInputResponse(signals)
	case phaseAwaitingDelegateStarts:
		return e.acceptDelegateStarts(signals)
	case phaseAwaitingDelegateWaitOpen:
		return e.acceptDelegateWaitOpen(signals)
	case phaseWaitingDelegates:
		return e.acceptDelegates(signals)
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

func (e *execution) acceptModel(signals []agent.Signal) (agent.Transition, error) {
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
		return e.rejectTruncatedToolCalls(consumedSignals, response, calls)
	}

	e.state.PendingModelResponse = response
	e.state.NextToolCallIndex = 0
	e.state.ActiveToolCallEndIndex = 0
	e.state.SettledToolResults = nil
	e.state.DirectToolResultEligible = true
	return e.advanceToolCallBatch(consumedSignals)
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
	consumedSignals uint32,
	response *chat.Response,
	calls []chat.ToolCall,
) (agent.Transition, error) {
	if response == nil || response.Output == nil || response.Output.Message == nil || len(calls) == 0 {
		return agent.Transition{}, fmt.Errorf("%w: invalid truncated ToolCall response", ErrInvalidExecutionState)
	}
	results := make([]chat.ToolResult, len(calls))
	for index := range calls {
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

func (e *execution) acceptTools(signals []agent.Signal) (agent.Transition, error) {
	envelope, steer, consumedSignals, err := collectExpectedSignal(signals, operationToolBatch)
	if err != nil {
		return agent.Transition{}, err
	}
	if addSteerErr := e.addSteer(steer); addSteerErr != nil {
		return agent.Transition{}, addSteerErr
	}
	calls, err := e.activeCallSegment()
	if err != nil {
		return agent.Transition{}, err
	}
	results := envelope.ToolResult.Results
	if checkpoint := envelope.ToolResult.Checkpoint; checkpoint != nil {
		if validateErr := checkpoint.validate(calls); validateErr != nil {
			return agent.Transition{}, validateErr
		}
		return e.requestInputWait(consumedSignals, checkpoint)
	}
	if validateToolResultsErr := validateToolResults(calls, results); validateToolResultsErr != nil {
		return agent.Transition{}, validateToolResultsErr
	}
	advertisedToolNames, err := mergeAdvertisedToolNames(
		e.state.AdvertisedToolNames,
		envelope.ToolResult.AdvertisedToolNames,
	)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("%w: advertised Tools: %w", ErrInvalidExecutionState, err)
	}
	e.state.AdvertisedToolNames = advertisedToolNames
	e.state.SettledToolResults = append(e.state.SettledToolResults, results...)
	e.state.NextToolCallIndex = e.state.ActiveToolCallEndIndex
	e.state.DirectToolResultEligible = e.state.DirectToolResultEligible && envelope.ToolResult.Direct
	e.state.ToolCheckpoint = nil
	e.state.WaitID = nil
	return e.advanceToolCallBatch(consumedSignals)
}

func (e *execution) requestInputWait(
	consumedSignals uint32,
	checkpoint *toolCheckpoint,
) (agent.Transition, error) {
	if checkpoint == nil {
		return agent.Transition{}, fmt.Errorf("%w: missing Tool checkpoint", ErrInvalidExecutionState)
	}
	calls, err := e.activeCallSegment()
	if err != nil {
		return agent.Transition{}, err
	}
	if validateErr := checkpoint.validate(calls); validateErr != nil {
		return agent.Transition{}, validateErr
	}
	waitOpened := checkpoint.InputRequest
	payload, err := encodeProtocol(signalEnvelope{
		Operation:  operationWaitOpened,
		WaitOpened: &waitOpened,
	})
	if err != nil {
		return agent.Transition{}, err
	}
	waitKey, err := checkpointWaitKey(e.state.ModelCallCount, calls[checkpoint.NextToolCallIndex].ID, checkpoint.PauseCount)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.RequestWait(waitKey, payload)
	if err != nil {
		return agent.Transition{}, err
	}
	cloned := checkpoint.clone()
	e.state.ToolCheckpoint = &cloned
	e.state.WaitID = nil
	e.state.Phase = phaseAwaitingInputWaitOpen
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) acceptInputWaitOpen(signals []agent.Signal) (agent.Transition, error) {
	envelope, steer, consumedSignals, err := collectExpectedSignal(signals, operationWaitOpened)
	if err != nil {
		return agent.Transition{}, err
	}
	if addSteerErr := e.addSteer(steer); addSteerErr != nil {
		return agent.Transition{}, addSteerErr
	}
	var waitID agent.WaitID
	var ok bool
	for _, signal := range signals {
		decoded, decodeErr := decodeSignal(signal.Payload())
		if decodeErr == nil && decoded.Operation == operationWaitOpened {
			waitID, ok = signal.WaitID()
			break
		}
	}
	if !ok {
		return agent.Transition{}, fmt.Errorf("%w: Engine did not attach a WaitID", ErrInvalidExecutionState)
	}
	want, err := e.state.ToolCheckpoint.InputRequest.inputRequest()
	if err != nil {
		return agent.Transition{}, err
	}
	got, err := envelope.WaitOpened.inputRequest()
	if err != nil || !sameInputRequest(want, got) {
		return agent.Transition{}, fmt.Errorf("%w: wait-opened payload does not match Tool checkpoint", ErrInvalidExecutionState)
	}
	e.state.WaitID = &waitID
	e.state.Phase = phaseWaitingInput
	return agent.Wait(consumedSignals, waitID)
}

func (e *execution) acceptInputResponse(signals []agent.Signal) (agent.Transition, error) {
	envelope, steer, consumedSignals, err := collectExpectedSignal(signals, operationInputResponse)
	if err != nil {
		return agent.Transition{}, err
	}
	if addSteerErr := e.addSteer(steer); addSteerErr != nil {
		return agent.Transition{}, addSteerErr
	}
	inputSignal, ok := signalForOperation(signals, operationInputResponse)
	if !ok {
		return agent.Transition{}, fmt.Errorf("%w: input-response Signal is missing", ErrInvalidExecutionState)
	}
	waitID, addressed := inputSignal.WaitID()
	if !addressed || e.state.WaitID == nil || waitID != *e.state.WaitID {
		return agent.Transition{}, fmt.Errorf("%w: input response addressed the wrong wait", ErrInvalidExecutionState)
	}
	request, err := e.state.ToolCheckpoint.InputRequest.inputRequest()
	if err != nil {
		return agent.Transition{}, err
	}
	response, err := request.validateResponse(envelope.InputResponse)
	if err != nil {
		return agent.Transition{}, err
	}
	calls, err := e.activeCallSegment()
	if err != nil {
		return agent.Transition{}, err
	}
	effectEnvelope, err := newToolBatchEffect(
		e.state.ModelCallCount,
		e.state.NextToolCallIndex,
		calls,
		e.state.ToolCheckpoint,
		response,
	)
	if err != nil {
		return agent.Transition{}, err
	}
	payload, err := encodeProtocol(effectEnvelope)
	if err != nil {
		return agent.Transition{}, err
	}
	effect, err := agent.NewDispatcherEffect(payload)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.WaitID = nil
	e.state.Phase = phaseAwaitingTools
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) complete(consumedSignals uint32, output Output) (agent.Transition, error) {
	if err := output.Validate(); err != nil {
		return agent.Transition{}, err
	}
	encoded, err := agent.EncodeOutput(output)
	if err != nil {
		return agent.Transition{}, err
	}
	e.state.Phase = phaseCompleted
	e.clearToolCallBatch()
	e.state.WaitID = nil
	e.state.PendingSteer = nil
	e.state.FinalOutput = &output
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
	e.clearToolCallBatch()
	e.state.WaitID = nil
	e.state.PendingSteer = nil
	e.state.Phase = phaseReadyModel
	return e.requestModel(consumedSignals, nil)
}

func (e *execution) advanceToolCallBatch(consumedSignals uint32) (agent.Transition, error) {
	for {
		calls, assistant, err := responseToolCalls(e.state.PendingModelResponse)
		if err != nil || uint64(len(calls)) > uint64(^uint32(0)) ||
			uint64(e.state.NextToolCallIndex) > uint64(len(calls)) {
			return agent.Transition{}, fmt.Errorf("%w: invalid pending ToolCall batch", ErrInvalidExecutionState)
		}
		if e.state.NextToolCallIndex == uint32(len(calls)) {
			return e.finishToolCallBatch(consumedSignals, assistant)
		}
		if _, delegated := e.definition.delegate(calls[e.state.NextToolCallIndex].Name); delegated {
			e.state.DirectToolResultEligible = false
			transition, started, err := e.startDelegateSegment(consumedSignals, calls)
			if err != nil {
				return agent.Transition{}, err
			}
			if started {
				return transition, nil
			}
			continue
		}
		return e.requestToolCallSegment(consumedSignals, calls)
	}
}

func (e *execution) finishToolCallBatch(
	consumedSignals uint32,
	assistant *chat.Message,
) (agent.Transition, error) {
	results := cloneToolResults(e.state.SettledToolResults)
	completionContext := []chat.Message{assistant.Clone(), chat.NewToolMessage(results...)}
	direct := e.state.DirectToolResultEligible && e.state.PendingSteer == nil
	e.clearToolCallBatch()
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

func (e *execution) requestToolCallSegment(
	consumedSignals uint32,
	calls []chat.ToolCall,
) (agent.Transition, error) {
	end := e.state.NextToolCallIndex + 1
	for end < uint32(len(calls)) {
		if _, delegated := e.definition.delegate(calls[end].Name); delegated {
			break
		}
		end++
	}
	envelope, err := newToolBatchEffect(
		e.state.ModelCallCount,
		e.state.NextToolCallIndex,
		calls[e.state.NextToolCallIndex:end],
		nil,
		nil,
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
	e.state.ActiveToolCallEndIndex = end
	e.state.Phase = phaseAwaitingTools
	return agent.Continue(consumedSignals, effect)
}

func (e *execution) activeCallSegment() ([]chat.ToolCall, error) {
	calls, _, err := responseToolCalls(e.state.PendingModelResponse)
	if err != nil || e.state.NextToolCallIndex >= e.state.ActiveToolCallEndIndex ||
		uint64(e.state.ActiveToolCallEndIndex) > uint64(len(calls)) {
		return nil, fmt.Errorf("%w: invalid active ToolCall segment", ErrInvalidExecutionState)
	}
	return calls[e.state.NextToolCallIndex:e.state.ActiveToolCallEndIndex], nil
}

func (e *execution) clearToolCallBatch() {
	e.state.PendingModelResponse = nil
	e.state.NextToolCallIndex = 0
	e.state.ActiveToolCallEndIndex = 0
	e.state.SettledToolResults = nil
	e.state.DirectToolResultEligible = false
	e.state.ToolCheckpoint = nil
	e.state.DelegateSegment = nil
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

func signalForOperation(signals []agent.Signal, expected operation) (agent.Signal, bool) {
	for _, signal := range signals {
		envelope, err := decodeSignal(signal.Payload())
		if err == nil && envelope.Operation == expected {
			return signal, true
		}
	}
	return agent.Signal{}, false
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

func validateToolResults(calls []chat.ToolCall, results []chat.ToolResult) error {
	if len(results) != len(calls) {
		return fmt.Errorf("%w: %d tool results do not match %d calls", ErrInvalidExecutionState, len(results), len(calls))
	}
	for index := range calls {
		if results[index].ID != calls[index].ID || results[index].Name != calls[index].Name {
			return fmt.Errorf("%w: tool result %d does not match call %q", ErrInvalidExecutionState, index, calls[index].ID)
		}
	}
	return nil
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
