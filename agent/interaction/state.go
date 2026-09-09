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
	phaseReadyModel               phase = "ready_model"
	phaseAwaitingModel            phase = "awaiting_model"
	phaseAwaitingToolStarts       phase = "awaiting_tool_starts"
	phaseAwaitingToolWaitOpen     phase = "awaiting_tool_wait_open"
	phaseWaitingTools             phase = "waiting_tools"
	phaseAwaitingDelegateStarts   phase = "awaiting_delegate_starts"
	phaseAwaitingDelegateWaitOpen phase = "awaiting_delegate_wait_open"
	phaseWaitingDelegates         phase = "waiting_delegates"
	phaseCompleted                phase = "completed"
)

func (p phase) valid() bool {
	switch p {
	case phaseReadyModel, phaseAwaitingModel, phaseAwaitingToolStarts,
		phaseAwaitingToolWaitOpen, phaseWaitingTools, phaseAwaitingDelegateStarts,
		phaseAwaitingDelegateWaitOpen, phaseWaitingDelegates, phaseCompleted:
		return true
	default:
		return false
	}
}

// executionState is the complete Strategy-owned recovery state. WorkingContext
// is self-sufficient for the next model call. PendingModelResponse is present
// only while a model-requested ToolCall batch is being settled.
type executionState struct {
	Phase                    phase                 `json:"phase"`
	WorkingContext           *chat.Request         `json:"working_context"`
	ModelCallCount           uint32                `json:"model_call_count"`
	AdvertisedToolNames      []string              `json:"advertised_tool_names,omitempty"`
	PendingModelResponse     *chat.Response        `json:"pending_model_response,omitempty"`
	ActiveToolCallEndIndex   uint32                `json:"active_tool_call_end_index,omitempty"`
	SettledToolResults       []chat.ToolResult     `json:"settled_tool_results,omitempty"`
	DirectToolResultEligible bool                  `json:"direct_tool_result_eligible,omitempty"`
	ToolSegment              *toolSegmentState     `json:"tool_segment,omitempty"`
	DelegateSegment          *delegateSegmentState `json:"delegate_segment,omitempty"`
	WaitID                   *agent.WaitID         `json:"wait_id,omitempty"`
	PendingSteer             *steerBatch           `json:"pending_steer,omitempty"`
	ArtifactRecords          []artifactRecord      `json:"artifact_records,omitempty"`
	FinalModelResponse       *chat.Response        `json:"final_model_response,omitempty"`
	FinalToolResults         []chat.ToolResult     `json:"final_tool_results,omitempty"`
}

type artifactRecord struct {
	ModelCallSequence uint32       `json:"model_call_sequence"`
	ToolCallIndex     uint32       `json:"tool_call_index"`
	ToolCallID        string       `json:"tool_call_id"`
	DelegateName      string       `json:"delegate_name"`
	Output            agent.Output `json:"output"`
}

type delegateInvocationState struct {
	ChildKey       *agent.ChildKey  `json:"child_key,omitempty"`
	ChildProcessID *agent.ProcessID `json:"child_process_id,omitempty"`
	ToolResult     *chat.ToolResult `json:"tool_result,omitempty"`
}

type delegateSegmentState struct {
	Invocations []delegateInvocationState `json:"invocations"`
}

func (e executionState) Validate(definition *Definition) error {
	if err := e.validateEnvelope(definition); err != nil {
		return err
	}
	if err := e.validateArtifacts(definition); err != nil {
		return err
	}
	return e.validatePhaseState(definition)
}

func (e executionState) validateEnvelope(definition *Definition) error {
	if !definition.valid() {
		return ErrInvalidExecutionState
	}
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
	if e.ModelCallCount > definition.maxModelCalls {
		return fmt.Errorf("%w: model call count exceeds configured limit", ErrInvalidExecutionState)
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
	case phaseAwaitingToolStarts, phaseAwaitingToolWaitOpen, phaseWaitingTools,
		phaseAwaitingDelegateStarts, phaseAwaitingDelegateWaitOpen, phaseWaitingDelegates:
		return e.validateActiveCallState(definition)
	case phaseCompleted:
		return e.validateCompletedState()
	}
	return nil
}

func (e executionState) validateReadyModelState() error {
	if e.hasPendingBatch() || e.WaitID != nil || e.PendingSteer != nil || e.hasFinalOutput() {
		return fmt.Errorf("%w: ready_model has inconsistent pending response or limit", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateAwaitingModelState() error {
	if e.hasPendingBatch() || e.WaitID != nil || e.PendingSteer != nil || e.hasFinalOutput() || e.ModelCallCount == 0 {
		return fmt.Errorf("%w: awaiting_model has inconsistent pending response or limit", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateActiveCallState(definition *Definition) error {
	calls, err := e.validatePendingBatch()
	if err != nil {
		return err
	}
	active := calls[e.nextToolCallIndex():e.ActiveToolCallEndIndex]
	delegateSegment := e.delegatePhase()
	for _, call := range active {
		_, delegated := definition.delegate(call.Name)
		if delegated != delegateSegment {
			return fmt.Errorf("%w: active call segment mixes Tool and Delegate ownership", ErrInvalidExecutionState)
		}
	}
	if delegateSegment {
		return e.validateDelegateCallState(active)
	}
	return e.validateToolCallState(active, definition)
}

func (e executionState) delegatePhase() bool {
	return e.Phase == phaseAwaitingDelegateStarts ||
		e.Phase == phaseAwaitingDelegateWaitOpen || e.Phase == phaseWaitingDelegates
}

func (e executionState) validateDelegateCallState(active []chat.ToolCall) error {
	if e.ToolSegment != nil || e.DelegateSegment == nil {
		return fmt.Errorf("%w: Delegate phase has inconsistent batch state", ErrInvalidExecutionState)
	}
	if err := e.DelegateSegment.validate(e.Phase, active); err != nil {
		return err
	}
	switch e.Phase {
	case phaseAwaitingDelegateStarts:
		if e.WaitID != nil {
			return fmt.Errorf("%w: awaiting Delegate starts contains a WaitID", ErrInvalidExecutionState)
		}
	case phaseAwaitingDelegateWaitOpen:
		if e.WaitID != nil {
			return fmt.Errorf("%w: awaiting Delegate wait contains a WaitID", ErrInvalidExecutionState)
		}
	case phaseWaitingDelegates:
		if e.WaitID == nil || !e.WaitID.Valid() {
			return fmt.Errorf("%w: waiting_delegates requires an Engine WaitID", ErrInvalidExecutionState)
		}
	}
	return nil
}

func (e executionState) validateToolCallState(active []chat.ToolCall, definition *Definition) error {
	if e.DelegateSegment != nil || e.ToolSegment == nil {
		return fmt.Errorf("%w: Tool phase has inconsistent child state", ErrInvalidExecutionState)
	}
	if err := e.ToolSegment.validate(e.Phase, active, e.ModelCallCount, definition); err != nil {
		return err
	}
	if e.Phase == phaseWaitingTools {
		if e.WaitID == nil || !e.WaitID.Valid() {
			return fmt.Errorf("%w: waiting Tools require an Engine WaitID", ErrInvalidExecutionState)
		}
	} else if e.WaitID != nil {
		return fmt.Errorf("%w: Tool start or wait opening already has a WaitID", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) nextToolCallIndex() uint32 {
	return uint32(len(e.SettledToolResults))
}

func (e executionState) hasFinalOutput() bool {
	return e.FinalModelResponse != nil || len(e.FinalToolResults) != 0
}

func (e executionState) output() Output {
	output := Output{
		ModelCalls: e.ModelCallCount, ModelResponse: e.FinalModelResponse,
		DirectToolResults: e.FinalToolResults,
	}
	switch {
	case e.FinalModelResponse != nil:
		output.Source = CompletionSourceModelResponse
	case len(e.FinalToolResults) != 0:
		output.Source = CompletionSourceDirectToolResults
	}
	return output
}

func (e executionState) validateCompletedState() error {
	if e.hasPendingBatch() || e.WaitID != nil || e.PendingSteer != nil {
		return fmt.Errorf("%w: completed state requires only its final Output", ErrInvalidExecutionState)
	}
	if err := e.output().Validate(); err != nil {
		return fmt.Errorf("%w: final Output: %w", ErrInvalidExecutionState, err)
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
	if e.PendingModelResponse == nil || len(e.ArtifactRecords) == 0 ||
		e.ArtifactRecords[len(e.ArtifactRecords)-1].ModelCallSequence != e.ModelCallCount {
		return nil
	}
	calls, _, err := responseToolCalls(e.PendingModelResponse)
	if err != nil {
		return fmt.Errorf("%w: current-round artifact has no pending ToolCall batch", ErrInvalidExecutionState)
	}
	for _, artifact := range e.ArtifactRecords {
		if artifact.ModelCallSequence != e.ModelCallCount {
			continue
		}
		if uint64(artifact.ToolCallIndex) >= uint64(len(calls)) ||
			uint64(artifact.ToolCallIndex) >= uint64(len(e.SettledToolResults)) {
			return fmt.Errorf("%w: current-round artifact is not settled", ErrInvalidExecutionState)
		}
		call := calls[artifact.ToolCallIndex]
		if call.ID != artifact.ToolCallID || call.Name != artifact.DelegateName {
			return fmt.Errorf("%w: current-round artifact does not match ToolCall", ErrInvalidExecutionState)
		}
		if _, found := definition.delegate(call.Name); !found {
			return fmt.Errorf("%w: current-round artifact is not a Delegate output", ErrInvalidExecutionState)
		}
		result := e.SettledToolResults[artifact.ToolCallIndex]
		if result.IsError || result.ID != call.ID || result.Name != call.Name ||
			!bytes.Equal(result.Output.Details, artifact.Output.JSON()) || len(result.Output.Content) != 0 {
			return fmt.Errorf("%w: current-round artifact does not match settled result", ErrInvalidExecutionState)
		}
	}
	return nil
}

func (e executionState) hasPendingBatch() bool {
	return e.PendingModelResponse != nil || e.ActiveToolCallEndIndex != 0 ||
		len(e.SettledToolResults) != 0 || e.DirectToolResultEligible || e.ToolSegment != nil ||
		e.DelegateSegment != nil
}

func (e executionState) validatePendingBatch() ([]chat.ToolCall, error) {
	if e.PendingModelResponse == nil || e.hasFinalOutput() || e.ModelCallCount == 0 {
		return nil, fmt.Errorf("%w: active call phase requires a model response", ErrInvalidExecutionState)
	}
	if err := e.PendingModelResponse.Validate(); err != nil {
		return nil, fmt.Errorf("%w: pending response: %w", ErrInvalidExecutionState, err)
	}
	if e.PendingModelResponse.Output.FinishReason != chat.FinishReasonToolCalls {
		return nil, fmt.Errorf("%w: pending response must finish with tool_calls", ErrInvalidExecutionState)
	}
	calls, _, err := responseToolCalls(e.PendingModelResponse)
	if err != nil || len(calls) == 0 || uint64(len(calls)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("%w: pending response has no bounded unambiguous tool calls", ErrInvalidExecutionState)
	}
	if uint64(len(e.SettledToolResults)) >= uint64(e.ActiveToolCallEndIndex) || uint64(e.ActiveToolCallEndIndex) > uint64(len(calls)) {
		return nil, fmt.Errorf("%w: ToolCall cursor is inconsistent", ErrInvalidExecutionState)
	}
	if err := validateToolResults(calls[:e.nextToolCallIndex()], e.SettledToolResults); err != nil {
		return nil, err
	}
	if !e.DirectToolResultEligible && e.nextToolCallIndex() == 0 && e.Phase == phaseAwaitingToolStarts {
		return nil, fmt.Errorf("%w: fresh Tool batch lost its direct-result candidate", ErrInvalidExecutionState)
	}
	return calls, nil
}

func (d delegateSegmentState) validate(current phase, calls []chat.ToolCall) error {
	if len(d.Invocations) != len(calls) {
		return fmt.Errorf("%w: Delegate batch length does not match calls", ErrInvalidExecutionState)
	}
	pending := 0
	started := 0
	for index, invocation := range d.Invocations {
		if invocation.ChildKey != nil && !invocation.ChildKey.Valid() ||
			invocation.ChildProcessID != nil && !invocation.ChildProcessID.Valid() {
			return fmt.Errorf("%w: Delegate call %d has invalid Framework identity", ErrInvalidExecutionState, index)
		}
		if invocation.ToolResult != nil {
			if err := invocation.ToolResult.Validate(); err != nil ||
				invocation.ToolResult.ID != calls[index].ID || invocation.ToolResult.Name != calls[index].Name ||
				!invocation.ToolResult.IsError {
				return fmt.Errorf("%w: Delegate result %d does not match its call", ErrInvalidExecutionState, index)
			}
		}
		switch {
		case invocation.ChildKey != nil && invocation.ChildProcessID == nil && invocation.ToolResult == nil:
			pending++
		case invocation.ChildKey != nil && invocation.ChildProcessID != nil && invocation.ToolResult == nil:
			started++
		case invocation.ChildProcessID == nil && invocation.ToolResult != nil:
		default:
			return fmt.Errorf("%w: Delegate call %d has contradictory settlement", ErrInvalidExecutionState, index)
		}
	}
	if current == phaseAwaitingDelegateStarts && (pending == 0 || started != 0) ||
		current != phaseAwaitingDelegateStarts && pending != 0 ||
		(current == phaseAwaitingDelegateWaitOpen || current == phaseWaitingDelegates) && started == 0 {
		return fmt.Errorf("%w: Delegate batch phase does not match settlements", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) activeDelegateCalls() ([]chat.ToolCall, error) {
	if e.Phase != phaseAwaitingDelegateStarts &&
		e.Phase != phaseAwaitingDelegateWaitOpen &&
		e.Phase != phaseWaitingDelegates ||
		e.WorkingContext == nil || e.ModelCallCount == 0 ||
		e.hasFinalOutput() || e.ToolSegment != nil || e.DelegateSegment == nil {
		return nil, ErrInvalidExecutionState
	}
	if err := e.WorkingContext.Validate(); err != nil || len(e.WorkingContext.Tools) != 0 {
		return nil, ErrInvalidExecutionState
	}
	if e.PendingSteer != nil {
		if err := e.PendingSteer.validate(); err != nil {
			return nil, err
		}
	}
	calls, err := e.validatePendingBatch()
	if err != nil {
		return nil, err
	}
	active := calls[e.nextToolCallIndex():e.ActiveToolCallEndIndex]
	if err := e.DelegateSegment.validate(e.Phase, active); err != nil {
		return nil, err
	}
	switch e.Phase {
	case phaseAwaitingDelegateStarts, phaseAwaitingDelegateWaitOpen:
		if e.WaitID != nil {
			return nil, ErrInvalidExecutionState
		}
	case phaseWaitingDelegates:
		if e.WaitID == nil || !e.WaitID.Valid() {
			return nil, ErrInvalidExecutionState
		}
	}
	return active, nil
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
