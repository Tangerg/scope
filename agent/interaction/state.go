package interaction

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// phase names the next protocol step. It is derived from the recovery facts
// and never stored, so no persisted marker can disagree with them.
type phase uint8

const (
	phaseReadyModel phase = iota
	phaseAwaitingModel
	phaseAwaitingContext
	phaseAdvancingTools
	phaseRoundComplete
	phaseAwaitingCalls
)

type contextPreparation struct {
	Applied []run.SignalID `json:"applied,omitempty"`
}

// executionState is the complete Strategy-owned recovery state. WorkingContext
// owns messages and options; the Program owns Tools. ToolRound owns each pending round.
type executionState struct {
	PendingEffect       *run.EffectID       `json:"pending_effect,omitzero"`
	Preparation         *contextPreparation `json:"preparation,omitzero"`
	WorkingContext      *Input              `json:"working_context"`
	ModelCallCount      uint64              `json:"model_call_count" jsonwire:"required"`
	AdvertisedToolNames []string            `json:"advertised_tool_names,omitempty"`
	ToolRound           *toolCallRound      `json:"tool_round,omitzero"`
	PendingSteer        *steerBatch         `json:"pending_steer,omitzero"`
	ArtifactRecords     []artifactRecord    `json:"artifact_records,omitempty"`
}

// phase relies on every model request advancing ModelCallCount in the Step
// that emits it: without a round, a counted call is in flight.
func (e executionState) phase() phase {
	switch {
	case e.Preparation != nil:
		return phaseAwaitingContext
	case e.ToolRound == nil && e.ModelCallCount == 0:
		return phaseReadyModel
	case e.ToolRound == nil:
		return phaseAwaitingModel
	case e.ToolRound.Batch != nil:
		return e.ToolRound.Batch.phase()
	case e.ToolRound.answered():
		return phaseRoundComplete
	default:
		return phaseAdvancingTools
	}
}

type artifactRecord struct {
	ModelCallSequence uint64      `json:"model_call_sequence"`
	ToolCallID        string      `json:"tool_call_id"`
	DelegateName      string      `json:"delegate_name"`
	Output            run.Payload `json:"output"`
}

func (a artifactRecord) validate(definition *definition, modelCallCount uint64) error {
	delegate, found := definition.delegate(a.DelegateName)
	if a.ModelCallSequence == 0 || a.ModelCallSequence > modelCallCount || a.ToolCallID == "" || !found || !a.Output.Valid() {
		return errors.New("invalid identity or output")
	}
	if err := delegate.validateOutput(a.Output); err != nil {
		return fmt.Errorf("violates Delegate output contract: %w", err)
	}
	return nil
}

// follows keeps records in model-call order; within one model call the slice
// keeps the ToolCall order in which the round recorded them.
func (a artifactRecord) follows(previous artifactRecord) bool {
	return a.ModelCallSequence >= previous.ModelCallSequence
}

func (e executionState) validate(ctx context.Context, definition *definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !definition.valid() {
		return fmt.Errorf("%w: valid definition is required", ErrInvalidExecutionState)
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
	if err := e.validatePhaseState(ctx, definition); err != nil {
		return err
	}
	if err := e.validateProposal(ctx, definition); err != nil {
		return err
	}
	return e.validateRoundDelegateOutputs(ctx, definition)
}

func (e executionState) validateProposal(ctx context.Context, definition *definition) error {
	if e.ToolRound == nil {
		return nil
	}
	calls, err := validatedToolCalls(e.ToolRound.Response)
	if err != nil {
		return fmt.Errorf("%w: model proposal: %w", ErrInvalidExecutionState, err)
	}
	if e.ToolRound.rejected() {
		return nil
	}
	if e.ToolRound.truncated() {
		return fmt.Errorf("%w: truncated proposal was not rejected atomically", ErrInvalidExecutionState)
	}
	rejection, err := definition.admitProposals(ctx, calls, e.AdvertisedToolNames)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: model proposal: %w", ErrInvalidExecutionState, err)
	}
	if rejection != "" {
		return fmt.Errorf("%w: %s", ErrInvalidExecutionState, rejection)
	}
	return nil
}

func (e executionState) validateRoundDelegateOutputs(ctx context.Context, definition *definition) error {
	if e.ToolRound == nil {
		return nil
	}
	calls, err := validatedToolCalls(e.ToolRound.Response)
	if err != nil {
		return fmt.Errorf("%w: model proposal: %w", ErrInvalidExecutionState, err)
	}
	if err := e.ToolRound.validateResults(ctx, calls); err != nil {
		return err
	}
	for index, result := range e.ToolRound.Results {
		delegate, delegated := definition.delegate(calls[index].Name)
		if !delegated || result.Disposition != ResultSucceeded {
			continue
		}
		if _, err := delegate.admitResultOutput(result.Output); err != nil {
			return fmt.Errorf("%w: round Delegate result %d: %w", ErrInvalidExecutionState, index, err)
		}
	}
	return nil
}

func (e executionState) validateEnvelope() error {
	pending := e.phase() == phaseAwaitingContext || e.phase() == phaseAwaitingModel
	if pending != (e.PendingEffect != nil) || e.PendingEffect != nil && !e.PendingEffect.Valid() {
		return fmt.Errorf("%w: pending model effect does not match phase", ErrInvalidExecutionState)
	}
	if e.WorkingContext == nil {
		return fmt.Errorf("%w: WorkingContext is required", ErrInvalidExecutionState)
	}
	if err := e.WorkingContext.Validate(); err != nil {
		return fmt.Errorf("%w: WorkingContext: %w", ErrInvalidExecutionState, err)
	}
	if e.PendingSteer != nil {
		if err := e.PendingSteer.validate(); err != nil {
			return fmt.Errorf("%w: pending steer: %w", ErrInvalidExecutionState, err)
		}
	}
	return nil
}

func (e executionState) validatePhaseState(ctx context.Context, definition *definition) error {
	switch e.phase() {
	case phaseAdvancingTools, phaseRoundComplete:
		return e.validateRoundBoundary(ctx)
	case phaseAwaitingContext:
		if !definition.prepareContext || e.ToolRound != nil || !definition.maxModelCalls.Allows(e.ModelCallCount, 1) {
			return ErrInvalidExecutionState
		}
		if len(e.Preparation.Applied) > 0 {
			return validateSteerSignalIDs(e.Preparation.Applied)
		}
		return nil
	case phaseReadyModel, phaseAwaitingModel:
		return e.validateModelState()
	case phaseAwaitingCalls:
		return e.validateActiveCallState(ctx, definition)
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
	if !opensToolRound(e.ToolRound.Response.Output.FinishReason) {
		return ErrInvalidExecutionState
	}
	return e.ToolRound.validateResults(ctx, calls)
}

// Steering admitted during a model call waits for its outcome; before the
// first request, the same Step applies all admitted steering immediately.
func (e executionState) validateModelState() error {
	if e.phase() == phaseReadyModel && e.PendingSteer != nil {
		return fmt.Errorf("%w: fresh model phase retains pending steering", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateActiveCallState(ctx context.Context, definition *definition) error {
	active, err := e.activeCalls(ctx)
	if err != nil {
		return err
	}
	calls, err := validatedToolCalls(e.ToolRound.Response)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	return e.ToolRound.Batch.validateBindings(ctx, definition, active, calls[e.ToolRound.nextCallIndex():], e.ModelCallCount)
}

func (e executionState) activeCalls(ctx context.Context) ([]chat.ToolCall, error) {
	if e.ModelCallCount == 0 {
		return nil, fmt.Errorf("%w: active calls require a model call", ErrInvalidExecutionState)
	}
	active, err := e.ToolRound.activeCalls(ctx)
	if err != nil {
		return nil, err
	}
	if err := e.ToolRound.Batch.validate(ctx, active); err != nil {
		return nil, err
	}
	return active, nil
}

func (e *executionState) replaceModelContext(messages []chat.Message) error {
	effective := e.WorkingContext.clone()
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
	request := e.WorkingContext.clone()
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

func (e *executionState) applyPendingSteer() ([]run.SignalID, error) {
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

func (e executionState) validateArtifacts(ctx context.Context, definition *definition) error {
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
func (e *executionState) recordRoundArtifacts(definition *definition) error {
	calls, err := validatedToolCalls(e.ToolRound.Response)
	if err != nil {
		return err
	}
	for index, result := range e.ToolRound.Results {
		delegate, delegated := definition.delegate(calls[index].Name)
		if !delegated || result.Disposition != ResultSucceeded {
			continue
		}
		output, err := delegate.admitResultOutput(result.Output)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
		}
		e.ArtifactRecords = append(e.ArtifactRecords, artifactRecord{
			ModelCallSequence: e.ModelCallCount, ToolCallID: calls[index].ID, DelegateName: calls[index].Name, Output: output,
		})
	}
	return nil
}

func (e executionState) snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(executionStateKind, e)
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
	return response.Output.Message.ToolCalls(), nil
}
