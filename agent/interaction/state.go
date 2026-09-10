package interaction

import (
	"bytes"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

type phase string

const (
	phaseReadyModel            phase = "ready_model"
	phaseAwaitingModel         phase = "awaiting_model"
	phaseAwaitingChildStarts   phase = "awaiting_child_starts"
	phaseAwaitingChildWaitOpen phase = "awaiting_child_wait_open"
	phaseWaitingChildren       phase = "waiting_children"
	phaseCompleted             phase = "completed"
)

func (p phase) valid() bool {
	switch p {
	case phaseReadyModel, phaseAwaitingModel, phaseAwaitingChildStarts,
		phaseAwaitingChildWaitOpen, phaseWaitingChildren, phaseCompleted:
		return true
	default:
		return false
	}
}

// executionState is the complete Strategy-owned recovery state. WorkingContext
// is self-sufficient for the next model call; ToolRound owns each pending round.
type executionState struct {
	Phase               phase            `json:"phase"`
	WorkingContext      *chat.Request    `json:"working_context"`
	ModelCallCount      uint32           `json:"model_call_count"`
	AdvertisedToolNames []string         `json:"advertised_tool_names,omitempty"`
	ToolRound           *toolCallRound   `json:"tool_round,omitempty"`
	PendingSteer        *steerBatch      `json:"pending_steer,omitempty"`
	ArtifactRecords     []artifactRecord `json:"artifact_records,omitempty"`
	FinalOutput         *Output          `json:"final_output,omitempty"`
}

type artifactRecord struct {
	ModelCallSequence uint32       `json:"model_call_sequence"`
	ToolCallIndex     uint32       `json:"tool_call_index"`
	ToolCallID        string       `json:"tool_call_id"`
	DelegateName      string       `json:"delegate_name"`
	Output            agent.Output `json:"output"`
}

func (e executionState) Validate(definition *Definition) error {
	if !definition.valid() {
		return ErrInvalidExecutionState
	}
	if e.ModelCallCount > definition.maxModelCalls {
		return fmt.Errorf("%w: model call count exceeds configured limit", ErrInvalidExecutionState)
	}
	if err := e.validateEnvelope(); err != nil {
		return err
	}
	if err := e.validateArtifacts(definition); err != nil {
		return err
	}
	return e.validatePhaseState(definition)
}

func (e executionState) validateEnvelope() error {
	if !e.Phase.valid() {
		return fmt.Errorf("%w: unknown phase %q", ErrInvalidExecutionState, e.Phase)
	}
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

func (e executionState) validatePhaseState(definition *Definition) error {
	switch e.Phase {
	case phaseReadyModel:
		return e.validateReadyModelState()
	case phaseAwaitingModel:
		return e.validateAwaitingModelState()
	case phaseAwaitingChildStarts, phaseAwaitingChildWaitOpen, phaseWaitingChildren:
		return e.validateActiveCallState(definition)
	case phaseCompleted:
		return e.validateCompletedState()
	}
	return nil
}

func (e executionState) validateReadyModelState() error {
	if e.ToolRound != nil || e.PendingSteer != nil || e.FinalOutput != nil {
		return fmt.Errorf("%w: ready_model has inconsistent pending response or limit", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateAwaitingModelState() error {
	if e.ToolRound != nil || e.PendingSteer != nil || e.FinalOutput != nil || e.ModelCallCount == 0 {
		return fmt.Errorf("%w: awaiting_model has inconsistent pending response or limit", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateActiveCallState(definition *Definition) error {
	active, err := e.activeChildCalls()
	if err != nil {
		return err
	}
	return e.ToolRound.ChildBatch.validateBindings(definition, active)
}

func (e executionState) activeChildCalls() ([]chat.ToolCall, error) {
	if e.Phase != phaseAwaitingChildStarts && e.Phase != phaseAwaitingChildWaitOpen && e.Phase != phaseWaitingChildren ||
		e.FinalOutput != nil || e.ModelCallCount == 0 {
		return nil, ErrInvalidExecutionState
	}
	active, err := e.ToolRound.activeCalls()
	if err != nil {
		return nil, err
	}
	if err := e.ToolRound.ChildBatch.validate(e.Phase, active, e.ModelCallCount); err != nil {
		return nil, err
	}
	if !e.ToolRound.DirectResultEligible && e.ToolRound.nextCallIndex() == 0 &&
		e.Phase == phaseAwaitingChildStarts && e.ToolRound.ChildBatch.Kind == childCallsTool {
		return nil, fmt.Errorf("%w: fresh Tool batch lost its direct-result candidate", ErrInvalidExecutionState)
	}
	return active, nil
}

func (e *executionState) complete(output Output) {
	e.Phase = phaseCompleted
	e.ToolRound = nil
	e.PendingSteer = nil
	e.FinalOutput = &output
}

func (e executionState) validateCompletedState() error {
	if e.ToolRound != nil || e.PendingSteer != nil || e.FinalOutput == nil {
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

func (e executionState) validateArtifacts(definition *Definition) error {
	var previousModelCallSequence uint32
	var previousToolCallIndex uint32
	type artifactIdentity struct {
		modelCallSequence uint32
		toolCallID        string
	}
	seen := make(map[artifactIdentity]struct{}, len(e.ArtifactRecords))
	for index, artifact := range e.ArtifactRecords {
		delegate, found := definition.delegate(artifact.DelegateName)
		if artifact.ModelCallSequence == 0 || artifact.ModelCallSequence > e.ModelCallCount ||
			artifact.ToolCallID == "" || !found || !artifact.Output.Valid() {
			return fmt.Errorf("%w: artifact %d has invalid identity or output", ErrInvalidExecutionState, index)
		}
		if index > 0 && (artifact.ModelCallSequence < previousModelCallSequence ||
			artifact.ModelCallSequence == previousModelCallSequence && artifact.ToolCallIndex <= previousToolCallIndex) {
			return fmt.Errorf("%w: artifacts are not in strict ToolCall order", ErrInvalidExecutionState)
		}
		identity := artifactIdentity{
			modelCallSequence: artifact.ModelCallSequence,
			toolCallID:        artifact.ToolCallID,
		}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("%w: duplicate artifact ToolCall identity", ErrInvalidExecutionState)
		}
		seen[identity] = struct{}{}
		if err := delegate.outputSchema.ValidateOutput(artifact.Output); err != nil {
			return fmt.Errorf("%w: artifact %d violates Delegate output contract", ErrInvalidExecutionState, index)
		}
		previousModelCallSequence = artifact.ModelCallSequence
		previousToolCallIndex = artifact.ToolCallIndex
	}
	return e.validateCurrentBatchArtifacts(definition)
}

func (e executionState) validateCurrentBatchArtifacts(definition *Definition) error {
	if e.ToolRound == nil || len(e.ArtifactRecords) == 0 ||
		e.ArtifactRecords[len(e.ArtifactRecords)-1].ModelCallSequence != e.ModelCallCount {
		return nil
	}
	calls, _, err := responseToolCalls(e.ToolRound.Response)
	if err != nil {
		return fmt.Errorf("%w: current-round artifact has no pending ToolCall batch", ErrInvalidExecutionState)
	}
	for _, artifact := range e.ArtifactRecords {
		if artifact.ModelCallSequence != e.ModelCallCount {
			continue
		}
		if uint64(artifact.ToolCallIndex) >= uint64(len(calls)) ||
			uint64(artifact.ToolCallIndex) >= uint64(len(e.ToolRound.Results)) {
			return fmt.Errorf("%w: current-round artifact is not settled", ErrInvalidExecutionState)
		}
		call := calls[artifact.ToolCallIndex]
		if call.ID != artifact.ToolCallID || call.Name != artifact.DelegateName {
			return fmt.Errorf("%w: current-round artifact does not match ToolCall", ErrInvalidExecutionState)
		}
		if _, found := definition.delegate(call.Name); !found {
			return fmt.Errorf("%w: current-round artifact is not a Delegate output", ErrInvalidExecutionState)
		}
		result := e.ToolRound.Results[artifact.ToolCallIndex]
		if result.IsError || result.ID != call.ID || result.Name != call.Name ||
			!bytes.Equal(result.Output.Details, artifact.Output.JSON()) || len(result.Output.Content) != 0 {
			return fmt.Errorf("%w: current-round artifact does not match settled result", ErrInvalidExecutionState)
		}
	}
	return nil
}

func cloneMessages(messages []chat.Message) []chat.Message {
	cloned := make([]chat.Message, len(messages))
	for index := range messages {
		cloned[index] = messages[index].Clone()
	}
	return cloned
}

func responseToolCalls(response *chat.Response) ([]chat.ToolCall, *chat.Message, error) {
	if response == nil {
		return nil, nil, errors.New("interaction: model returned a nil response")
	}
	if err := response.Validate(); err != nil {
		return nil, nil, fmt.Errorf("interaction: invalid model response: %w", err)
	}
	var calls []chat.ToolCall
	var message *chat.Message
	seenCallIDs := make(map[string]struct{})
	if response.Output == nil || response.Output.Message == nil {
		return nil, nil, nil
	}
	for _, part := range response.Output.Message.Parts {
		if part.Kind == chat.PartToolCall {
			if _, duplicate := seenCallIDs[part.ToolCall.ID]; duplicate {
				return nil, nil, fmt.Errorf("interaction: duplicate tool call ID %q", part.ToolCall.ID)
			}
			seenCallIDs[part.ToolCall.ID] = struct{}{}
			calls = append(calls, *part.ToolCall)
		}
	}
	if len(calls) > 0 {
		cloned := response.Output.Message.Clone()
		message = &cloned
	}
	return calls, message, nil
}
