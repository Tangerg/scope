package workflow

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

// phase names the next protocol step. It is derived from the recorded
// progress, never persisted, so no stored phase can contradict that progress.
type phase uint8

const (
	phaseReady phase = iota
	phaseChild
	phaseFanout
)

type executionState struct {
	StageIndex uint32 `json:"stage_index" jsonwire:"required"`
	// CurrentValue feeds the current Stage.
	CurrentValue           run.Payload        `json:"current_value" jsonwire:"required"`
	SelectedCaseID         string             `json:"selected_case_id,omitempty"`
	Child                  *child.Invocation  `json:"child,omitzero"`
	ActiveFanoutWindow     []child.Invocation `json:"active_fanout_window,omitempty"`
	CompletedFanoutOutputs []run.Payload      `json:"completed_fanout_outputs,omitempty"`
	LoopIteration          uint64             `json:"loop_iteration,omitzero"`
}

func (e executionState) phase() phase {
	switch {
	case e.Child != nil:
		return phaseChild
	case e.ActiveFanoutWindow != nil:
		return phaseFanout
	default:
		return phaseReady
	}
}

func (e executionState) validate(ctx context.Context, definition *definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !definition.valid() {
		return fmt.Errorf("%w: definition is invalid", ErrInvalidExecutionState)
	}
	// The Runtime never restores a completed Execution, so a restorable state
	// always names a Stage that has yet to finish.
	if uint64(e.StageIndex) >= uint64(len(definition.stages)) {
		return fmt.Errorf("%w: stage index %d is not a pending Stage", ErrInvalidExecutionState, e.StageIndex)
	}
	if !e.CurrentValue.Valid() {
		return fmt.Errorf("%w: current value is required", ErrInvalidExecutionState)
	}
	if err := definition.stages[e.StageIndex].inputSchema.Validate(e.CurrentValue.JSON()); err != nil {
		return fmt.Errorf("%w: current value does not satisfy current Stage: %w", ErrInvalidExecutionState, err)
	}
	return e.validatePhaseState(ctx, definition)
}

func (e executionState) validatePhaseState(ctx context.Context, definition *definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch current := e.phase(); current {
	case phaseReady:
		if !e.noProgress() {
			return fmt.Errorf("%w: progress outside a child or fan-out step", ErrInvalidExecutionState)
		}
	case phaseChild:
		if !e.singleChildStage(definition) || e.hasFanoutProgress() || e.Child.Done() {
			return fmt.Errorf("%w: single-child progress does not match its Stage", ErrInvalidExecutionState)
		}
		if _, err := (child.Batch{*e.Child}).Parent(func(int) (run.ChildKey, error) { return e.childKey(definition.stages[e.StageIndex]) }); err != nil {
			return fmt.Errorf("%w: single child: %w", ErrInvalidExecutionState, err)
		}
	default:
		if e.hasSingleChildProgress() {
			return fmt.Errorf("%w: fan-out progress retains single-child progress", ErrInvalidExecutionState)
		}
		if err := e.validateFanout(ctx, definition, current); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (e executionState) singleChildStage(definition *definition) bool {
	if e.StageIndex >= uint32(len(definition.stages)) {
		return false
	}
	stage := definition.stages[e.StageIndex]
	switch stage.kind {
	case StageKindCall:
		return e.SelectedCaseID == "" && e.LoopIteration == 0
	case StageKindSwitch:
		_, found := stage.switcher.binding(e.SelectedCaseID)
		return found && e.LoopIteration == 0
	case StageKindLoop:
		return e.SelectedCaseID == "" && e.LoopIteration > 0 &&
			stage.loop.maxIterations.Allows(e.LoopIteration)
	default:
		return false
	}
}

func (e executionState) childKey(stage Stage) (run.ChildKey, error) {
	return workflowChildKey(singleBindingIdentityKind, stage.id, e.SelectedCaseID, strconv.FormatUint(e.LoopIteration, 10))
}

func (e executionState) noProgress() bool {
	return !e.hasSingleChildProgress() && !e.hasFanoutProgress()
}

func (e executionState) hasSingleChildProgress() bool {
	return e.SelectedCaseID != "" || e.Child != nil || e.LoopIteration != 0
}

func (e executionState) hasFanoutProgress() bool {
	return e.ActiveFanoutWindow != nil || e.CompletedFanoutOutputs != nil
}

func (e executionState) validateFanout(ctx context.Context, definition *definition, current phase) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stage, err := e.validateFanoutBoundary(ctx, definition)
	if err != nil {
		return err
	}
	windowStart := e.fanoutWindowStart()
	if _, err := child.Batch(e.ActiveFanoutWindow).Parent(func(position int) (run.ChildKey, error) {
		return stage.fanoutChildKey(windowStart + uint32(position))
	}); err != nil {
		return fmt.Errorf("%w: active fan-out window: %w", ErrInvalidExecutionState, err)
	}
	for index, invocation := range e.ActiveFanoutWindow {
		if err := ctx.Err(); err != nil {
			return err
		}
		if invocation.Termination == nil {
			continue
		}
		if output, completed := invocation.Termination.Output(); completed {
			if err := stage.fanout.validateMemberOutput(output); err != nil {
				return fmt.Errorf("%w: active fan-out output %d: %w", ErrInvalidExecutionState, index, err)
			}
		}
	}
	if err := e.validateCompletedFanoutOutputs(ctx, stage); err != nil {
		return err
	}
	return ctx.Err()
}

func (e executionState) fanoutWindowStart() uint32 {
	return uint32(len(e.CompletedFanoutOutputs))
}

func (e executionState) validateFanoutBoundary(ctx context.Context, definition *definition) (Stage, error) {
	if e.StageIndex >= uint32(len(definition.stages)) {
		return Stage{}, fmt.Errorf("%w: fan-out stage index exceeds stage count", ErrInvalidExecutionState)
	}
	stage := definition.stages[e.StageIndex]
	if stage.kind != StageKindFork && stage.kind != StageKindMap {
		return Stage{}, fmt.Errorf("%w: fan-out progress requires a fork or map stage", ErrInvalidExecutionState)
	}
	input, err := stage.fanout.source.prepareInput(ctx, e.CurrentValue)
	if err != nil {
		return Stage{}, fmt.Errorf("%w: fan-out count: %w", ErrInvalidExecutionState, err)
	}
	count := input.count()
	if uint64(len(e.CompletedFanoutOutputs)) >= uint64(count) {
		return Stage{}, fmt.Errorf("%w: completed fan-out outputs leave no active window", ErrInvalidExecutionState)
	}
	start, windowSize := e.fanoutWindowStart(), stage.fanout.windowSize
	if start%windowSize != 0 || uint64(len(e.ActiveFanoutWindow)) != uint64(fanoutWindowLen(start, count, windowSize)) {
		return Stage{}, fmt.Errorf("%w: active fan-out window does not match source boundaries", ErrInvalidExecutionState)
	}
	return stage, nil
}

func (e executionState) validateCompletedFanoutOutputs(ctx context.Context, stage Stage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for index, output := range e.CompletedFanoutOutputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := stage.fanout.validateMemberOutput(output); err != nil {
			return fmt.Errorf("%w: completed fan-out output %d: %w", ErrInvalidExecutionState, index, err)
		}
	}
	return ctx.Err()
}

func (e *executionState) clearSingleChild() {
	e.SelectedCaseID = ""
	e.Child = nil
}

func (e *executionState) finishStage() {
	e.clearSingleChild()
	e.ActiveFanoutWindow = nil
	e.CompletedFanoutOutputs = nil
	e.LoopIteration = 0
	e.StageIndex++
}

func (e executionState) snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(executionStateKind, e)
}
