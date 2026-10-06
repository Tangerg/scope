package interaction

import (
	"context"
	"errors"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
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
	// Completed marks the Step that returned the final Output. The Engine owns
	// that Output; the state never repeats it.
	Completed bool `json:"completed,omitzero"`
}

func (e *executionState) UnmarshalJSON(data []byte) error {
	type wire executionState
	decoded, err := jsonwire.Decode[wire](data, "model_call_count")
	if err != nil {
		return err
	}
	*e = executionState(decoded)
	return nil
}

// phase relies on every model request advancing ModelCallCount in the Step
// that emits it: without a round or completion, a counted call is in flight.
func (e executionState) phase() phase {
	switch {
	case e.Completed:
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
	ToolCallID        string        `json:"tool_call_id"`
	DelegateName      string        `json:"delegate_name"`
	Output            agent.Payload `json:"output"`
}

func (a artifactRecord) validate(definition *Definition, modelCallCount uint64) error {
	delegate, found := definition.delegate(a.DelegateName)
	if a.ModelCallSequence == 0 || a.ModelCallSequence > modelCallCount || a.ToolCallID == "" || !found || !a.Output.Valid() {
		return errors.New("invalid identity or output")
	}
	if err := delegate.deployment.Descriptor().OutputSchema().Validate(a.Output.JSON()); err != nil {
		return fmt.Errorf("violates Delegate output contract: %w", err)
	}
	return nil
}

// follows keeps records in model-call order; within one model call the slice
// keeps the ToolCall order in which the round recorded them.
func (a artifactRecord) follows(previous artifactRecord) bool {
	return a.ModelCallSequence >= previous.ModelCallSequence
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
	if err := e.ToolRound.ChildBatch.validate(ctx, active, e.ModelCallCount); err != nil {
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

func (e *executionState) complete() {
	e.ToolRound = nil
	e.PendingSteer = nil
	e.Completed = true
}

func (e executionState) validateCompletedState() error {
	if e.ToolRound != nil || e.PendingSteer != nil || e.ModelCallCount == 0 {
		return fmt.Errorf("%w: completed state retains pending work or issued no model call", ErrInvalidExecutionState)
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
	// The current round's Delegate results are its Artifacts until it ends.
	latest := e.ModelCallCount
	if e.ToolRound != nil {
		latest--
	}
	for index, artifact := range e.ArtifactRecords {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := artifact.validate(definition, latest); err != nil {
			return fmt.Errorf("%w: artifact %d %w", ErrInvalidExecutionState, index, err)
		}
		if index > 0 && !artifact.follows(e.ArtifactRecords[index-1]) {
			return fmt.Errorf("%w: artifacts are not in model-call order", ErrInvalidExecutionState)
		}
		identity := artifactIdentity{modelCallSequence: artifact.ModelCallSequence, toolCallID: artifact.ToolCallID}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("%w: duplicate artifact ToolCall identity", ErrInvalidExecutionState)
		}
		seen[identity] = struct{}{}
	}
	return ctx.Err()
}

// recordRoundArtifacts outlives the round its successful Delegate results
// belong to. Until the round ends those results are the Artifacts, so a
// record is created only as the round's results leave for the model context.
func (e *executionState) recordRoundArtifacts(definition *Definition) error {
	calls, err := validatedToolCalls(e.ToolRound.Response)
	if err != nil {
		return err
	}
	for index, result := range e.ToolRound.Results {
		if _, delegated := definition.delegate(calls[index].Name); !delegated || result.Disposition != ResultSucceeded {
			continue
		}
		output, err := agent.ParsePayload(result.Output.Details)
		if err != nil {
			return fmt.Errorf("%w: Delegate result output: %w", ErrInvalidExecutionState, err)
		}
		e.ArtifactRecords = append(e.ArtifactRecords, artifactRecord{
			ModelCallSequence: e.ModelCallCount, ToolCallID: calls[index].ID, DelegateName: calls[index].Name, Output: output,
		})
	}
	return nil
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
