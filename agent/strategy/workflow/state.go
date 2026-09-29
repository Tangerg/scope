package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

type phase string

const (
	phaseReady                  phase = "ready"
	phaseChild                  phase = "child"
	phaseAwaitingFanoutStarts   phase = "awaiting_fanout_starts"
	phaseAwaitingFanoutWaitOpen phase = "awaiting_fanout_wait_open"
	phaseWaitingFanout          phase = "waiting_fanout"
	phaseCompleted              phase = "completed"
)

func (p phase) valid() bool {
	switch p {
	case phaseReady, phaseChild, phaseAwaitingFanoutStarts, phaseAwaitingFanoutWaitOpen,
		phaseWaitingFanout, phaseCompleted:
		return true
	default:
		return false
	}
}

type executionState struct {
	Phase                  phase              `json:"phase"`
	StageIndex             uint32             `json:"stage_index"`
	CurrentValue           json.RawMessage    `json:"current_value"`
	SelectedCaseID         string             `json:"selected_case_id,omitempty"`
	Child                  *childcall.Single  `json:"child,omitzero"`
	FanoutWaitID           *agent.WaitID      `json:"fanout_wait_id,omitzero"`
	ActiveFanoutWindow     []fanoutChildState `json:"active_fanout_window,omitempty"`
	CompletedFanoutOutputs []json.RawMessage  `json:"completed_fanout_outputs,omitempty"`
	LoopIteration          uint64             `json:"loop_iteration,omitzero"`
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

func (f fanoutChildState) child(key agent.ChildKey, deployment agent.DeploymentRef) childcall.Child {
	child := childcall.Child{Key: key, Deployment: deployment, Done: f.ChildProcessID == nil && f.Failure != nil}
	if f.ChildProcessID != nil {
		child.ProcessID = *f.ChildProcessID
	}
	return child
}

func (e executionState) validate(ctx context.Context, definition *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !e.Phase.valid() {
		return fmt.Errorf("%w: unknown phase %q", ErrInvalidExecutionState, e.Phase)
	}
	if !definition.valid() {
		return fmt.Errorf("%w: definition is invalid", ErrInvalidExecutionState)
	}
	if uint64(e.StageIndex) > uint64(len(definition.stages)) {
		return fmt.Errorf("%w: stage index %d exceeds stage count", ErrInvalidExecutionState, e.StageIndex)
	}
	input, err := agent.ParsePayload(e.CurrentValue)
	if err != nil {
		return fmt.Errorf("%w: current value: %w", ErrInvalidExecutionState, err)
	}
	if e.StageIndex < uint32(len(definition.stages)) {
		if err := definition.stages[e.StageIndex].inputSchema.Validate(input.JSON()); err != nil {
			return fmt.Errorf("%w: current value does not satisfy current Stage: %w", ErrInvalidExecutionState, err)
		}
	} else if err := definition.descriptor.ValidateOutput(input); err != nil {
		return fmt.Errorf("%w: final value schema: %w", ErrInvalidExecutionState, err)
	}
	return e.validatePhaseState(ctx, definition)
}

func (e executionState) validatePhaseState(ctx context.Context, definition *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch e.Phase {
	case phaseReady:
		if e.StageIndex >= uint32(len(definition.stages)) || !e.noProgress() {
			return fmt.Errorf("%w: ready phase requires an unfinished stage without retained progress", ErrInvalidExecutionState)
		}
	case phaseChild:
		if !e.singleChildStage(definition) || e.Child == nil || e.hasFanoutProgress() {
			return fmt.Errorf("%w: child phase requires matching single-child progress", ErrInvalidExecutionState)
		}
	case phaseAwaitingFanoutStarts, phaseAwaitingFanoutWaitOpen, phaseWaitingFanout:
		if e.hasSingleChildProgress() {
			return fmt.Errorf("%w: fan-out phase retains single-child progress", ErrInvalidExecutionState)
		}
		if err := e.validateFanout(ctx, definition); err != nil {
			return err
		}
	case phaseCompleted:
		if e.StageIndex != uint32(len(definition.stages)) || !e.noProgress() {
			return fmt.Errorf("%w: completed phase requires all stages finished without retained progress", ErrInvalidExecutionState)
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

func (e executionState) validateFanout(ctx context.Context, definition *Definition) error {
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
	if err := e.validateFanoutPhase(window); err != nil {
		return err
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
	// failedAfterStart counts children whose completion failed after a start;
	// they can exist only once the window wait has opened.
	failedAfterStart int
}

func (e executionState) validateFanoutChildren(ctx context.Context) (fanoutWindowSummary, error) {
	if err := ctx.Err(); err != nil {
		return fanoutWindowSummary{}, err
	}
	var summary fanoutWindowSummary
	settled := 0
	started := make(map[agent.ProcessID]struct{}, len(e.ActiveFanoutWindow))
	for index, child := range e.ActiveFanoutWindow {
		if err := ctx.Err(); err != nil {
			return fanoutWindowSummary{}, err
		}
		if child.settled() {
			settled++
		}
		if child.ChildProcessID == nil {
			continue
		}
		if _, duplicate := started[*child.ChildProcessID]; duplicate {
			return fanoutWindowSummary{}, fmt.Errorf("%w: fan-out child %d reuses process %q", ErrInvalidExecutionState, index, *child.ChildProcessID)
		}
		started[*child.ChildProcessID] = struct{}{}
		if child.Failure != nil {
			summary.failedAfterStart++
		}
	}
	if settled != 0 && settled != len(e.ActiveFanoutWindow) {
		return fanoutWindowSummary{}, fmt.Errorf("%w: fan-out window retains partially applied starts", ErrInvalidExecutionState)
	}
	summary.settled, summary.started = settled != 0, len(started)
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

func (e executionState) validateFanoutPhase(window fanoutWindowSummary) error {
	switch e.Phase {
	case phaseAwaitingFanoutStarts:
		if e.FanoutWaitID != nil || window.settled && window.started != 0 {
			return fmt.Errorf("%w: awaiting starts phase retains started children or a wait identity", ErrInvalidExecutionState)
		}
	case phaseAwaitingFanoutWaitOpen:
		if e.FanoutWaitID != nil || !window.settled || window.started == 0 {
			return fmt.Errorf("%w: awaiting wait phase requires settled starts and no wait identity", ErrInvalidExecutionState)
		}
		if window.failedAfterStart != 0 {
			return fmt.Errorf("%w: fan-out child failed before its wait opened", ErrInvalidExecutionState)
		}
	case phaseWaitingFanout:
		if e.FanoutWaitID == nil || !window.settled || window.started == 0 {
			return fmt.Errorf("%w: waiting phase requires a wait identity and settled starts", ErrInvalidExecutionState)
		}
	default:
		return fmt.Errorf("%w: phase %q cannot carry fan-out progress", ErrInvalidExecutionState, e.Phase)
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

func (e *executionState) finishStage(stageCount uint32) {
	e.clearSingleChild()
	e.ActiveFanoutWindow = nil
	e.CompletedFanoutOutputs = nil
	e.LoopIteration = 0
	e.StageIndex++
	e.Phase = phaseReady
	if e.StageIndex == stageCount {
		e.Phase = phaseCompleted
	}
}

func (e executionState) snapshot() (agent.ExecutionState, error) {
	return agent.EncodeExecutionState(executionStateKind, e)
}
