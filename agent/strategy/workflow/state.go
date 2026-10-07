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
	StageIndex uint32 `json:"stage_index"`
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
	decoded, err := jsonwire.Decode[wire](data, "stage_index")
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
	if start%windowSize != 0 || uint64(len(e.ActiveFanoutWindow)) != uint64(min(windowSize, count-start)) {
		return Stage{}, fmt.Errorf("%w: active fan-out window does not match source boundaries", ErrInvalidExecutionState)
	}
	return stage, nil
}

// fanoutWindowSummary is derived from the active window. A window is adopted
// atomically, so its starts are either all settled or all pending.
type fanoutWindowSummary struct {
	settled bool
	started int
	// failedAfterStart is legal only after the window wait has opened.
	failedAfterStart int
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
		summary.started++
		if child.Failure != nil {
			summary.failedAfterStart++
		}
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

// validateFanoutWindow checks what the derived phase cannot: a started member
// fails only after its wait opens, and only started children are awaited. A
// window whose starts all failed is the state its failing Step commits.
func validateFanoutWindow(current phase, window fanoutWindowSummary) error {
	switch current {
	case phaseAwaitingFanoutWaitOpen:
		if window.failedAfterStart != 0 {
			return fmt.Errorf("%w: fan-out child failed before its wait opened", ErrInvalidExecutionState)
		}
	case phaseWaitingFanout:
		if !window.settled || window.started == 0 {
			return fmt.Errorf("%w: fan-out wait requires started children", ErrInvalidExecutionState)
		}
	}
	return nil
}

func (e *executionState) clearSingleChild() {
	e.SelectedCaseID = ""
	e.Child = nil
}

func (e executionState) firstFanoutFailure() agent.Failure {
	for _, child := range e.ActiveFanoutWindow {
		if child.Failure != nil {
			return *child.Failure
		}
	}
	return agent.Failure{}
}

func (e executionState) fanoutHasStartedChildren() bool {
	for _, child := range e.ActiveFanoutWindow {
		if child.ChildProcessID != nil {
			return true
		}
	}
	return false
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
