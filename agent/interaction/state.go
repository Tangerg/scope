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
	PendingEffect       run.EffectID        `json:"pending_effect,omitzero"`
	Preparation         *contextPreparation `json:"preparation,omitzero"`
	WorkingContext      Input               `json:"working_context"`
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
	if err := e.validateRound(ctx, definition); err != nil {
		return err
	}
	return e.validatePhaseState(ctx, definition)
}

// validateRound admits the open round once: its proposal, recorded results,
// admission and Delegate outputs. Live Steps then read the round unchecked.
func (e executionState) validateRound(ctx context.Context, definition *definition) error {
	if e.ToolRound == nil {
		return nil
	}
	if e.ModelCallCount == 0 {
		return fmt.Errorf("%w: a tool round requires a model call", ErrInvalidExecutionState)
	}
	if err := e.ToolRound.validateProposal(); err != nil {
		return fmt.Errorf("%w: model proposal: %w", ErrInvalidExecutionState, err)
	}
	calls := e.ToolRound.calls()
	if err := e.ToolRound.validateResults(ctx, calls); err != nil {
		return err
	}
	if err := e.validateAdmission(ctx, definition, calls); err != nil {
		return err
	}
	return e.validateRoundDelegateOutputs(definition, calls)
}

func (e executionState) validateAdmission(ctx context.Context, definition *definition, calls []chat.ToolCall) error {
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

func (e executionState) validateRoundDelegateOutputs(definition *definition, calls []chat.ToolCall) error {
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
	if pending != e.PendingEffect.Valid() {
		return fmt.Errorf("%w: pending model effect does not match phase", ErrInvalidExecutionState)
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

// validatePhaseState checks what each phase adds to the round validateRound
// already admitted.
func (e executionState) validatePhaseState(ctx context.Context, definition *definition) error {
	switch e.phase() {
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

// Steering admitted during a model call waits for its outcome; before the
// first request, the same Step applies all admitted steering immediately.
func (e executionState) validateModelState() error {
	if e.phase() == phaseReadyModel && e.PendingSteer != nil {
		return fmt.Errorf("%w: fresh model phase retains pending steering", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateActiveCallState(ctx context.Context, definition *definition) error {
	round := e.ToolRound
	if err := round.validateBatchCursor(); err != nil {
		return err
	}
	active := round.batchCalls()
	if err := round.Batch.validate(ctx, active); err != nil {
		return err
	}
	return round.Batch.validateBindings(ctx, definition, active, round.calls()[round.nextCallIndex():], e.ModelCallCount)
}

// replaceModelContext and appendToContext take ownership of messages and keep
// WorkingContext a valid model request. A failed check fails the Step, which
// ends the Execution, so neither restores the previous context.
func (e *executionState) replaceModelContext(messages []chat.Message) error {
	e.WorkingContext.Messages = messages
	if err := e.WorkingContext.Validate(); err != nil {
		return fmt.Errorf("%w: replacement model context: %w", ErrInvalidExecutionState, err)
	}
	return nil
}

func (e *executionState) appendToContext(purpose string, messages ...chat.Message) error {
	e.WorkingContext.Messages = append(e.WorkingContext.Messages, messages...)
	if err := e.WorkingContext.Validate(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidExecutionState, purpose, err)
	}
	return nil
}

// addSteer queues steering collected from delivered Signals. Collection
// validated each message, and the Runtime admits a SignalID once per Process,
// so the merged batch needs no further check; Restore validates a stored one.
func (e *executionState) addSteer(batch steerBatch) {
	if batch.empty() {
		return
	}
	if e.PendingSteer == nil {
		e.PendingSteer = &batch
		return
	}
	e.PendingSteer.Messages = append(e.PendingSteer.Messages, batch.Messages...)
	e.PendingSteer.SignalIDs = append(e.PendingSteer.SignalIDs, batch.SignalIDs...)
}

func (e *executionState) applyPendingSteer() ([]run.SignalID, error) {
	if e.PendingSteer == nil {
		return nil, nil
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
// The Runtime held each output to its Delegate's Program and Restore admitted
// the round, so the record only changes representation.
func (e *executionState) recordRoundArtifacts(definition *definition) error {
	calls := e.ToolRound.calls()
	for index, result := range e.ToolRound.Results {
		if _, delegated := definition.delegate(calls[index].Name); !delegated || result.Disposition != ResultSucceeded {
			continue
		}
		output, err := run.ParsePayload(result.Output.Details)
		if err != nil {
			return fmt.Errorf("%w: Delegate result output: %w", ErrInvalidExecutionState, err)
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

// proposedCalls reads the ToolCalls of a response its codec or the model
// dispatcher already validated.
func proposedCalls(response *chat.Response) []chat.ToolCall {
	if response.Output.Message == nil {
		return nil
	}
	return response.Output.Message.ToolCalls()
}
