package interaction

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// phase names the next protocol step. It is derived from the recovery facts
// and never stored, so no persisted marker can disagree with them.
type phase uint8

const (
	phaseReadyModel phase = iota
	phaseAwaitingModel
	phaseAdvancingTools
	phaseRoundComplete
	phaseAwaitingChildStarts
	phaseAwaitingChildWaitOpen
	phaseWaitingChildren
	phaseCompleted
)

// executionState is the complete Strategy-owned recovery state. WorkingContext
// is self-sufficient for the next model call; ToolRound owns each pending round.
type executionState struct {
	WorkingContext      *chat.Request    `json:"working_context"`
	ModelCallCount      uint64           `json:"model_call_count"`
	AdvertisedToolNames []string         `json:"advertised_tool_names,omitempty"`
	ToolRound           *toolCallRound   `json:"tool_round,omitzero"`
	PendingSteer        *steerBatch      `json:"pending_steer,omitzero"`
	ArtifactRecords     []artifactRecord `json:"artifact_records,omitempty"`
	FinalOutput         *Output          `json:"final_output,omitzero"`
}

// phase relies on every model request advancing ModelCallCount in the Step
// that emits it: without a round or final Output, a counted call is in flight.
func (e executionState) phase() phase {
	switch {
	case e.FinalOutput != nil:
		return phaseCompleted
	case e.ToolRound == nil && e.ModelCallCount == 0:
		return phaseReadyModel
	case e.ToolRound == nil:
		return phaseAwaitingModel
	case e.ToolRound.ChildBatch != nil:
		return e.ToolRound.ChildBatch.phase()
	case e.ToolRound.answered():
		return phaseRoundComplete
	default:
		return phaseAdvancingTools
	}
}

type artifactRecord struct {
	ModelCallSequence uint64        `json:"model_call_sequence"`
	ToolCallIndex     uint32        `json:"tool_call_index"`
	ToolCallID        string        `json:"tool_call_id"`
	DelegateName      string        `json:"delegate_name"`
	Output            agent.Payload `json:"output"`
}

func (a artifactRecord) validate(definition *Definition, modelCallCount uint64) error {
	delegate, found := definition.delegate(a.DelegateName)
	if a.ModelCallSequence == 0 || a.ModelCallSequence > modelCallCount || a.ToolCallID == "" || !found || !a.Output.Valid() {
		return errors.New("invalid identity or output")
	}
	if err := delegate.outputSchema.Validate(a.Output.JSON()); err != nil {
		return fmt.Errorf("violates Delegate output contract: %w", err)
	}
	return nil
}

func (a artifactRecord) follows(previous artifactRecord) bool {
	return a.ModelCallSequence > previous.ModelCallSequence ||
		a.ModelCallSequence == previous.ModelCallSequence && a.ToolCallIndex > previous.ToolCallIndex
}

func (a artifactRecord) matchesSettled(call chat.ToolCall, result chat.ToolResult) bool {
	return call.ID == a.ToolCallID && call.Name == a.DelegateName && !result.IsError &&
		result.ID == call.ID && result.Name == call.Name &&
		bytes.Equal(result.Output.Details, a.Output.JSON()) && len(result.Output.Content) == 0
}

func (e executionState) validate(ctx context.Context, definition *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !definition.valid() {
		return fmt.Errorf("%w: valid Definition is required", ErrInvalidExecutionState)
	}
	if !definition.maxModelCalls.Allows(e.ModelCallCount) {
		return fmt.Errorf("%w: model call count exceeds configured limit", ErrInvalidExecutionState)
	}
	if err := e.validateEnvelope(); err != nil {
		return err
	}
	if err := definition.tools.validateAdvertisements(e.AdvertisedToolNames); err != nil {
		return fmt.Errorf("%w: advertised Tools: %w", ErrInvalidExecutionState, err)
	}
	if err := e.validateArtifacts(ctx, definition); err != nil {
		return err
	}
	return e.validatePhaseState(ctx, definition)
}

func (e executionState) validateEnvelope() error {
	if e.WorkingContext == nil {
		return fmt.Errorf("%w: WorkingContext is required", ErrInvalidExecutionState)
	}
	if err := e.WorkingContext.Validate(); err != nil {
		return fmt.Errorf("%w: WorkingContext: %w", ErrInvalidExecutionState, err)
	}
	if len(e.WorkingContext.Tools) != 0 {
		return fmt.Errorf("%w: executable tool definitions do not belong in WorkingContext", ErrInvalidExecutionState)
	}
	if err := validateAdvertisedToolNames(e.AdvertisedToolNames); err != nil {
		return fmt.Errorf("%w: advertised Tools: %w", ErrInvalidExecutionState, err)
	}
	if e.PendingSteer != nil {
		if err := e.PendingSteer.validate(); err != nil {
			return fmt.Errorf("%w: pending steer: %w", ErrInvalidExecutionState, err)
		}
	}
	return nil
}

func (e executionState) validatePhaseState(ctx context.Context, definition *Definition) error {
	switch e.phase() {
	case phaseAdvancingTools, phaseRoundComplete:
		return e.validateRoundBoundary(ctx)
	case phaseReadyModel, phaseAwaitingModel:
		return e.validateModelState()
	case phaseAwaitingChildStarts, phaseAwaitingChildWaitOpen, phaseWaitingChildren:
		return e.validateActiveCallState(ctx, definition)
	case phaseCompleted:
		return e.validateCompletedState()
	}
	return nil
}

func (e executionState) validateRoundBoundary(ctx context.Context) error {
	if e.ModelCallCount == 0 {
		return fmt.Errorf("%w: invalid round boundary", ErrInvalidExecutionState)
	}
	if e.phase() == phaseRoundComplete {
		return e.ToolRound.validateComplete(ctx)
	}
	calls, err := validatedToolCalls(e.ToolRound.Response)
	if err != nil || len(calls) == 0 {
		return fmt.Errorf("%w: round requires calls", ErrInvalidExecutionState)
	}
	finish := e.ToolRound.Response.Output.FinishReason
	if finish != chat.FinishReasonToolCalls && finish != chat.FinishReasonLength {
		return ErrInvalidExecutionState
	}
	return e.ToolRound.validateResults(ctx, calls)
}

// Steering waits for the next model request, so none can remain pending
// before the first call or while one is in flight.
func (e executionState) validateModelState() error {
	if e.PendingSteer != nil {
		return fmt.Errorf("%w: model call phase retains pending steering", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateActiveCallState(ctx context.Context, definition *Definition) error {
	active, err := e.activeChildCalls(ctx)
	if err != nil {
		return err
	}
	return e.ToolRound.ChildBatch.validateBindings(ctx, definition, active)
}

func (e executionState) activeChildCalls(ctx context.Context) ([]chat.ToolCall, error) {
	if e.ModelCallCount == 0 {
		return nil, fmt.Errorf("%w: active children require a model call", ErrInvalidExecutionState)
	}
	active, err := e.ToolRound.activeCalls(ctx)
	if err != nil {
		return nil, err
	}
	if err := e.ToolRound.ChildBatch.validate(ctx, e.phase(), active, e.ModelCallCount); err != nil {
		return nil, err
	}
	return active, nil
}

func (e *executionState) replaceModelContext(messages []chat.Message) error {
	effective := e.WorkingContext.Clone()
	effective.Messages = cloneMessages(messages)
	if err := effective.Validate(); err != nil {
		return fmt.Errorf("%w: replacement model context: %w", ErrInvalidExecutionState, err)
	}
	e.WorkingContext = effective
	return nil
}

// appendToContext keeps WorkingContext a valid model request: messages join
// it only when the extended request still validates.
func (e *executionState) appendToContext(purpose string, messages ...chat.Message) error {
	request := e.WorkingContext.Clone()
	request.Messages = append(request.Messages, cloneMessages(messages)...)
	if err := request.Validate(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidExecutionState, purpose, err)
	}
	e.WorkingContext = request
	return nil
}

func (e *executionState) addSteer(batch steerBatch) error {
	if batch.empty() {
		return nil
	}
	if err := batch.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if e.PendingSteer == nil {
		cloned := batch.clone()
		e.PendingSteer = &cloned
		return nil
	}
	e.PendingSteer.Messages = append(
		e.PendingSteer.Messages,
		cloneMessages(batch.Messages)...,
	)
	e.PendingSteer.SignalIDs = append(
		e.PendingSteer.SignalIDs,
		batch.SignalIDs...,
	)
	if err := e.PendingSteer.validate(); err != nil {
		return fmt.Errorf("%w: merged pending steer: %w", ErrInvalidExecutionState, err)
	}
	return nil
}

func (e *executionState) applyPendingSteer() ([]agent.SignalID, error) {
	if e.PendingSteer == nil {
		return nil, nil
	}
	if err := e.PendingSteer.validate(); err != nil {
		return nil, fmt.Errorf("%w: pending steer: %w", ErrInvalidExecutionState, err)
	}
	if err := e.appendToContext("steered model request", e.PendingSteer.Messages...); err != nil {
		return nil, err
	}
	appliedSignalIDs := slices.Clone(e.PendingSteer.SignalIDs)
	e.PendingSteer = nil
	return appliedSignalIDs, nil
}

func (e *executionState) complete(output Output) {
	e.ToolRound = nil
	e.PendingSteer = nil
	e.FinalOutput = &output
}

func (e executionState) validateCompletedState() error {
	if e.ToolRound != nil || e.PendingSteer != nil {
		return fmt.Errorf("%w: completed state requires only its final Output", ErrInvalidExecutionState)
	}
	if err := e.FinalOutput.Validate(); err != nil {
		return fmt.Errorf("%w: final Output: %w", ErrInvalidExecutionState, err)
	}
	if e.FinalOutput.ModelCalls != e.ModelCallCount {
		return fmt.Errorf("%w: final Output model calls differ from execution count", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateArtifacts(ctx context.Context, definition *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	type artifactIdentity struct {
		modelCallSequence uint64
		toolCallID        string
	}
	seen := make(map[artifactIdentity]struct{}, len(e.ArtifactRecords))
	for index, artifact := range e.ArtifactRecords {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := artifact.validate(definition, e.ModelCallCount); err != nil {
			return fmt.Errorf("%w: artifact %d %w", ErrInvalidExecutionState, index, err)
		}
		if index > 0 && !artifact.follows(e.ArtifactRecords[index-1]) {
			return fmt.Errorf("%w: artifacts are not in strict ToolCall order", ErrInvalidExecutionState)
		}
		identity := artifactIdentity{modelCallSequence: artifact.ModelCallSequence, toolCallID: artifact.ToolCallID}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("%w: duplicate artifact ToolCall identity", ErrInvalidExecutionState)
		}
		seen[identity] = struct{}{}
	}
	return e.validateCurrentBatchArtifacts(ctx)
}

func (e executionState) validateCurrentBatchArtifacts(ctx context.Context) error {
	if e.ToolRound == nil || len(e.ArtifactRecords) == 0 ||
		e.ArtifactRecords[len(e.ArtifactRecords)-1].ModelCallSequence != e.ModelCallCount {
		return nil
	}
	calls, err := validatedToolCalls(e.ToolRound.Response)
	if err != nil {
		return fmt.Errorf("%w: current-round artifact has no pending ToolCall batch", ErrInvalidExecutionState)
	}
	for _, artifact := range e.ArtifactRecords {
		if err := ctx.Err(); err != nil {
			return err
		}
		if artifact.ModelCallSequence != e.ModelCallCount {
			continue
		}
		if uint64(artifact.ToolCallIndex) >= uint64(len(calls)) ||
			uint64(artifact.ToolCallIndex) >= uint64(len(e.ToolRound.Results)) {
			return fmt.Errorf("%w: current-round artifact is not settled", ErrInvalidExecutionState)
		}
		if !artifact.matchesSettled(calls[artifact.ToolCallIndex], e.ToolRound.Results[artifact.ToolCallIndex].Result) {
			return fmt.Errorf("%w: current-round artifact does not match its settled ToolCall result", ErrInvalidExecutionState)
		}
	}
	return ctx.Err()
}

func (e executionState) snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState(executionStateKind, e)
}

func cloneMessages(messages []chat.Message) []chat.Message {
	cloned := make([]chat.Message, len(messages))
	for index := range messages {
		cloned[index] = messages[index].Clone()
	}
	return cloned
}

func validatedToolCalls(response *chat.Response) ([]chat.ToolCall, error) {
	if response == nil {
		return nil, errors.New("interaction: model returned a nil response")
	}
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("interaction: invalid model response: %w", err)
	}
	if response.Output == nil || response.Output.Message == nil {
		return nil, nil
	}
	var calls []chat.ToolCall
	seenCallIDs := make(map[string]struct{})
	for _, part := range response.Output.Message.Parts {
		if part.Kind != chat.PartToolCall {
			continue
		}
		if _, duplicate := seenCallIDs[part.ToolCall.ID]; duplicate {
			return nil, fmt.Errorf("interaction: duplicate tool call ID %q", part.ToolCall.ID)
		}
		seenCallIDs[part.ToolCall.ID] = struct{}{}
		calls = append(calls, *part.ToolCall)
	}
	return calls, nil
}
