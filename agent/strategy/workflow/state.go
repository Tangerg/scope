package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

// phase names the next protocol step. It is derived from the recorded
// progress, never persisted, so no stored phase can contradict that progress.
type phase uint8

const (
	phaseReady phase = iota
	phaseChild
	phaseAwaitingFanoutStarts
	phaseAwaitingFanoutWaitOpen
	phaseWaitingFanout
	phaseCompleted
)

type executionState struct {
	StageIndex uint32 `json:"stage_index" jsonwire:"required"`
	// CurrentValue feeds the current Stage; a completed workflow's value is
	// the Engine-owned Output and is not repeated here.
	CurrentValue           json.RawMessage    `json:"current_value,omitzero"`
	SelectedCaseID         string             `json:"selected_case_id,omitempty"`
	Child                  *childcall.Single  `json:"child,omitzero"`
	FanoutWaitID           *agent.WaitID      `json:"fanout_wait_id,omitzero"`
	ActiveFanoutWindow     []fanoutChildState `json:"active_fanout_window,omitempty"`
	CompletedFanoutOutputs []json.RawMessage  `json:"completed_fanout_outputs,omitempty"`
	LoopIteration          uint64             `json:"loop_iteration,omitzero"`
}

func (e *executionState) UnmarshalJSON(data []byte) error {
	type wire executionState
	decoded, err := jsonwire.Decode[wire](data)
	if err != nil {
		return err
	}
	*e = executionState(decoded)
	return nil
}

type fanoutChildState struct {
	ChildProcessID *agent.ProcessID `json:"child_process_id,omitzero"`
	Failure        *agent.Failure   `json:"failure,omitzero"`
}

func (f fanoutChildState) settled() bool {
	return f.ChildProcessID != nil || f.Failure != nil
}

func (f fanoutChildState) started() bool { return f.ChildProcessID != nil }

func (f *fanoutChildState) recordStart(start agent.ChildStartResult) {
	if failure, failed := start.Failure(); failed {
		f.Failure = &failure
	} else if id, started := start.ProcessID(); started {
		f.ChildProcessID = &id
	}
}

// fanoutBatch is the window's child batch at stage.
func (e executionState) fanoutBatch(stage Stage) (childcall.Batch, error) {
	batch := childcall.Batch{Children: make([]childcall.Child, len(e.ActiveFanoutWindow))}
	if e.FanoutWaitID != nil {
		batch.WaitID = *e.FanoutWaitID
	}
	for offset, progress := range e.ActiveFanoutWindow {
		key, err := fanoutChildKey(stage, e.fanoutWindowStart()+uint32(offset))
		if err != nil {
			return childcall.Batch{}, err
		}
		batch.Children[offset] = progress.child(key)
	}
	return batch, nil
}

func (f fanoutChildState) child(key agent.ChildKey) childcall.Child {
	child := childcall.Child{Key: key, Done: f.ChildProcessID == nil && f.Failure != nil}
	if f.ChildProcessID != nil {
		child.ProcessID = *f.ChildProcessID
	}
	return child
}

// phase derives the next protocol step of a Workflow with stageCount Stages.
// Fan-out starts settle as one batch, so one settled member marks them all.
func (e executionState) phase(stageCount uint32) phase {
	switch {
	case e.StageIndex == stageCount:
		return phaseCompleted
	case e.Child != nil:
		return phaseChild
	case e.FanoutWaitID != nil:
		return phaseWaitingFanout
	case e.ActiveFanoutWindow == nil:
		return phaseReady
	case slices.ContainsFunc(e.ActiveFanoutWindow, fanoutChildState.settled):
		return phaseAwaitingFanoutWaitOpen
	default:
		return phaseAwaitingFanoutStarts
	}
}

func (e executionState) validate(ctx context.Context, definition *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !definition.valid() {
		return fmt.Errorf("%w: definition is invalid", ErrInvalidExecutionState)
	}
	if uint64(e.StageIndex) > uint64(len(definition.stages)) {
		return fmt.Errorf("%w: stage index %d exceeds stage count", ErrInvalidExecutionState, e.StageIndex)
	}
	if e.StageIndex == uint32(len(definition.stages)) {
		if e.CurrentValue != nil {
			return fmt.Errorf("%w: completed workflow repeats its Output", ErrInvalidExecutionState)
		}
		return e.validatePhaseState(ctx, definition)
	}
	input, err := agent.ParsePayload(e.CurrentValue)
	if err != nil {
		return fmt.Errorf("%w: current value: %w", ErrInvalidExecutionState, err)
	}
	if err := definition.stages[e.StageIndex].inputSchema.Validate(input.JSON()); err != nil {
		return fmt.Errorf("%w: current value does not satisfy current Stage: %w", ErrInvalidExecutionState, err)
	}
	return e.validatePhaseState(ctx, definition)
}

func (e executionState) validatePhaseState(ctx context.Context, definition *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch current := e.phase(uint32(len(definition.stages))); current {
	case phaseReady, phaseCompleted:
		if !e.noProgress() {
			return fmt.Errorf("%w: progress outside a child or fan-out step", ErrInvalidExecutionState)
		}
	case phaseChild:
		if !e.singleChildStage(definition) || e.hasFanoutProgress() {
			return fmt.Errorf("%w: single-child progress does not match its Stage", ErrInvalidExecutionState)
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

func (e executionState) singleChildStage(definition *Definition) bool {
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

func (e executionState) noProgress() bool {
	return !e.hasSingleChildProgress() && !e.hasFanoutProgress()
}

func (e executionState) hasSingleChildProgress() bool {
	return e.SelectedCaseID != "" || e.Child != nil || e.LoopIteration != 0
}

func (e executionState) hasFanoutProgress() bool {
	return e.FanoutWaitID != nil || e.ActiveFanoutWindow != nil || e.CompletedFanoutOutputs != nil
}

func (e executionState) validateFanout(ctx context.Context, definition *Definition, current phase) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stage, err := e.validateFanoutBoundary(ctx, definition)
	if err != nil {
		return err
	}
	window, err := e.validateFanoutChildren(ctx)
	if err != nil {
		return err
	}
	if err := e.validateCompletedFanoutOutputs(ctx, stage); err != nil {
		return err
	}
	if err := validateFanoutWindow(current, window); err != nil {
		return err
	}
	// The child batch owns key and Process uniqueness across the window.
	batch, batchErr := e.fanoutBatch(stage)
	if batchErr == nil {
		batchErr = batch.Validate()
	}
	if batchErr != nil {
		return fmt.Errorf("%w: fan-out children: %w", ErrInvalidExecutionState, batchErr)
	}
	return ctx.Err()
}

func (e executionState) fanoutWindowStart() uint32 {
	return uint32(len(e.CompletedFanoutOutputs))
}

func (e executionState) validateFanoutBoundary(ctx context.Context, definition *Definition) (Stage, error) {
	if e.StageIndex >= uint32(len(definition.stages)) {
		return Stage{}, fmt.Errorf("%w: fan-out stage index exceeds stage count", ErrInvalidExecutionState)
	}
	stage := definition.stages[e.StageIndex]
	if stage.kind != StageKindFork && stage.kind != StageKindMap {
		return Stage{}, fmt.Errorf("%w: fan-out progress requires a fork or map stage", ErrInvalidExecutionState)
	}
	count, err := stage.fanout.source.count(ctx, e.CurrentValue)
	if err != nil {
		return Stage{}, fmt.Errorf("%w: fan-out count: %w", ErrInvalidExecutionState, err)
	}
	if uint64(len(e.CompletedFanoutOutputs)) >= uint64(count) {
		return Stage{}, fmt.Errorf("%w: completed fan-out outputs leave no active window", ErrInvalidExecutionState)
	}
	start, windowSize := e.fanoutWindowStart(), stage.fanout.windowSize
	if start%windowSize != 0 || uint64(len(e.ActiveFanoutWindow)) != uint64(fanoutWindowLen(start, count, windowSize)) {
		return Stage{}, fmt.Errorf("%w: active fan-out window does not match source boundaries", ErrInvalidExecutionState)
	}
	return stage, nil
}

// fanoutWindowSummary is derived from the active window. A window is adopted
// atomically, so its starts are either all settled or all pending.
type fanoutWindowSummary struct {
	settled bool
	started int
}

func (e executionState) validateFanoutChildren(ctx context.Context) (fanoutWindowSummary, error) {
	if err := ctx.Err(); err != nil {
		return fanoutWindowSummary{}, err
	}
	var summary fanoutWindowSummary
	settled := 0
	for _, child := range e.ActiveFanoutWindow {
		if err := ctx.Err(); err != nil {
			return fanoutWindowSummary{}, err
		}
		if child.settled() {
			settled++
		}
		if child.ChildProcessID == nil {
			continue
		}
		if child.Failure != nil {
			return fanoutWindowSummary{}, fmt.Errorf("%w: a started fan-out child repeats a Failure the Engine owns", ErrInvalidExecutionState)
		}
		summary.started++
	}
	if settled != 0 && settled != len(e.ActiveFanoutWindow) {
		return fanoutWindowSummary{}, fmt.Errorf("%w: fan-out window retains partially applied starts", ErrInvalidExecutionState)
	}
	summary.settled = settled != 0
	return summary, nil
}

func (e executionState) validateCompletedFanoutOutputs(ctx context.Context, stage Stage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for index, output := range e.CompletedFanoutOutputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		value, err := agent.ParsePayload(output)
		if err != nil {
			return fmt.Errorf("%w: completed fan-out output %d: %w", ErrInvalidExecutionState, index, err)
		}
		if err := stage.fanout.outputSchema.Validate(value.JSON()); err != nil {
			return fmt.Errorf("%w: completed fan-out output %d schema: %w", ErrInvalidExecutionState, index, err)
		}
	}
	return ctx.Err()
}

// validateFanoutWindow checks what the derived phase cannot: settled starts
// keep at least one started child, since a window with none failed at once,
// and only a settled window is awaited.
func validateFanoutWindow(current phase, window fanoutWindowSummary) error {
	if current == phaseWaitingFanout && !window.settled || window.settled && window.started == 0 {
		return fmt.Errorf("%w: fan-out wait requires started children", ErrInvalidExecutionState)
	}
	return nil
}

func (e *executionState) clearSingleChild() {
	e.SelectedCaseID = ""
	e.Child = nil
}

// firstFanoutFailure returns the first failure in window order, whether a
// member's start was refused or its outcome failed.
func firstFanoutFailure(window []fanoutChildState, outcomeFailures []*agent.Failure) agent.Failure {
	for offset, child := range window {
		if child.Failure != nil {
			return *child.Failure
		}
		if offset < len(outcomeFailures) && outcomeFailures[offset] != nil {
			return *outcomeFailures[offset]
		}
	}
	return agent.Failure{}
}

func (e *executionState) finishStage() {
	e.clearSingleChild()
	e.ActiveFanoutWindow = nil
	e.CompletedFanoutOutputs = nil
	e.LoopIteration = 0
	e.StageIndex++
}

func (e executionState) snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState(executionStateKind, e)
}
