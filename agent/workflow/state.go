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

// executionState runs at most one kind of child progress for its current
// Stage. Each variant carries only its own facts, so no single-child
// coordinate can survive into fan-out progress or outlive its child.
type executionState struct {
	StageIndex uint32 `json:"stage_index" jsonwire:"required"`
	// CurrentValue feeds the current Stage.
	CurrentValue run.Payload     `json:"current_value" jsonwire:"required"`
	Child        *singleChild    `json:"child,omitzero"`
	Fanout       *fanoutProgress `json:"fanout,omitzero"`
}

// singleChild is the one child a Call, Switch, or Loop Stage runs. Case names
// the selected Switch case and Iteration counts Loop iterations from one; both
// name the child, so a restored child must match them.
type singleChild struct {
	Case       string           `json:"case,omitempty"`
	Iteration  uint64           `json:"iteration,omitzero"`
	Invocation child.Invocation `json:"invocation"`
}

// fanoutProgress is the active window of a Fork or Map Stage after the outputs
// of every earlier window, which also place the window in the source.
type fanoutProgress struct {
	Window    []child.Invocation `json:"window"`
	Completed []run.Payload      `json:"completed,omitempty"`
}

func (e executionState) phase() phase {
	switch {
	case e.Child != nil:
		return phaseChild
	case e.Fanout != nil:
		return phaseFanout
	default:
		return phaseReady
	}
}

func (e executionState) validate(ctx context.Context, definition *definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The Runtime never restores a completed Execution, so a restorable state
	// always names a Stage that has yet to finish.
	if uint64(e.StageIndex) >= uint64(len(definition.stages)) {
		return fmt.Errorf("%w: stage index %d is not a pending Stage", ErrInvalidExecutionState, e.StageIndex)
	}
	if !e.CurrentValue.Valid() {
		return fmt.Errorf("%w: current value is required", ErrInvalidExecutionState)
	}
	stage := definition.stages[e.StageIndex]
	if err := stage.inputSchema.Validate(e.CurrentValue.JSON()); err != nil {
		return fmt.Errorf("%w: current value does not satisfy current Stage: %w", ErrInvalidExecutionState, err)
	}
	switch {
	case e.Child != nil && e.Fanout != nil:
		return fmt.Errorf("%w: single-child and fan-out progress at once", ErrInvalidExecutionState)
	case e.Child != nil:
		return e.Child.validate(stage)
	case e.Fanout != nil:
		return e.validateFanout(ctx, stage)
	default:
		return nil
	}
}

func (s singleChild) validate(stage Stage) error {
	if !s.Invocation.Valid() || !s.belongsTo(stage) || s.Invocation.Done() {
		return fmt.Errorf("%w: single-child progress does not match its Stage", ErrInvalidExecutionState)
	}
	if _, err := (child.Batch{s.Invocation}).Parent(func(int) (run.ChildKey, error) { return singleChildKey(stage, s.Case, s.Iteration) }); err != nil {
		return fmt.Errorf("%w: single child: %w", ErrInvalidExecutionState, err)
	}
	return nil
}

func (s singleChild) belongsTo(stage Stage) bool {
	switch stage.kind {
	case StageKindCall:
		return s.Case == "" && s.Iteration == 0
	case StageKindSwitch:
		_, found := stage.switcher.binding(s.Case)
		return found && s.Iteration == 0
	case StageKindLoop:
		return s.Case == "" && s.Iteration > 0 && stage.loop.maxIterations.Allows(s.Iteration)
	default:
		return false
	}
}

func singleChildKey(stage Stage, caseID string, iteration uint64) (run.ChildKey, error) {
	return workflowChildKey(singleBindingIdentityKind, stage.id, caseID, strconv.FormatUint(iteration, 10))
}

func (e executionState) validateFanout(ctx context.Context, stage Stage) error {
	if err := e.validateFanoutBoundary(ctx, stage); err != nil {
		return err
	}
	windowStart := e.fanoutWindowStart()
	if _, err := child.Batch(e.Fanout.Window).Parent(func(position int) (run.ChildKey, error) {
		_, key, err := stage.fanoutChild(windowStart + uint32(position))
		return key, err
	}); err != nil {
		return fmt.Errorf("%w: active fan-out window: %w", ErrInvalidExecutionState, err)
	}
	for index, invocation := range e.Fanout.Window {
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

// fanoutWindowStart is the source index of the window's first member: every
// earlier member has completed.
func (e executionState) fanoutWindowStart() uint32 {
	if e.Fanout == nil {
		return 0
	}
	return uint32(len(e.Fanout.Completed))
}

func (e executionState) validateFanoutBoundary(ctx context.Context, stage Stage) error {
	if stage.kind != StageKindFork && stage.kind != StageKindMap {
		return fmt.Errorf("%w: fan-out progress requires a fork or map stage", ErrInvalidExecutionState)
	}
	input, err := stage.fanout.source.prepareInput(ctx, e.CurrentValue)
	if err != nil {
		return fmt.Errorf("%w: fan-out count: %w", ErrInvalidExecutionState, err)
	}
	count := input.count()
	if uint64(len(e.Fanout.Completed)) >= uint64(count) {
		return fmt.Errorf("%w: completed fan-out outputs leave no active window", ErrInvalidExecutionState)
	}
	start, windowSize := e.fanoutWindowStart(), stage.fanout.windowSize
	if start%windowSize != 0 || uint64(len(e.Fanout.Window)) != uint64(fanoutWindowLen(start, count, windowSize)) {
		return fmt.Errorf("%w: active fan-out window does not match source boundaries", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateCompletedFanoutOutputs(ctx context.Context, stage Stage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for index, output := range e.Fanout.Completed {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := stage.fanout.validateMemberOutput(output); err != nil {
			return fmt.Errorf("%w: completed fan-out output %d: %w", ErrInvalidExecutionState, index, err)
		}
	}
	return ctx.Err()
}

func (e *executionState) finishStage() {
	e.Child, e.Fanout = nil, nil
	e.StageIndex++
}

func (e executionState) snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(executionStateKind, e)
}
