package workflow

import (
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

type phase string

const (
	phaseReady                  phase = "ready"
	phaseAwaitingChildStart     phase = "awaiting_child_start"
	phaseAwaitingChildWaitOpen  phase = "awaiting_child_wait_open"
	phaseWaitingChild           phase = "waiting_child"
	phaseAwaitingFanoutStarts   phase = "awaiting_fanout_starts"
	phaseAwaitingFanoutWaitOpen phase = "awaiting_fanout_wait_open"
	phaseWaitingFanout          phase = "waiting_fanout"
	phaseCompleted              phase = "completed"
)

func (p phase) valid() bool {
	switch p {
	case phaseReady, phaseAwaitingChildStart, phaseAwaitingChildWaitOpen,
		phaseWaitingChild, phaseAwaitingFanoutStarts, phaseAwaitingFanoutWaitOpen,
		phaseWaitingFanout, phaseCompleted:
		return true
	default:
		return false
	}
}

type executionState struct {
	Phase              phase              `json:"phase"`
	StageIndex         uint32             `json:"stage_index"`
	CurrentValue       json.RawMessage    `json:"current_value"`
	SelectedCaseID     string             `json:"selected_case_id,omitempty"`
	ChildProcessID     *agent.ProcessID   `json:"child_process_id,omitempty"`
	WaitID             *agent.WaitID      `json:"wait_id,omitempty"`
	NextFanoutIndex    uint32             `json:"next_fanout_index,omitempty"`
	ActiveFanoutWindow []fanoutChildState `json:"active_fanout_window,omitempty"`
	FanoutOutputs      []*json.RawMessage `json:"fanout_outputs,omitempty"`
	LoopIteration      uint32             `json:"loop_iteration,omitempty"`
}

type fanoutChildState struct {
	FanoutIndex    uint32           `json:"fanout_index"`
	ChildProcessID *agent.ProcessID `json:"child_process_id,omitempty"`
	Failure        *agent.Failure   `json:"failure,omitempty"`
}

func (e executionState) validate(definition *Definition) error {
	if !e.Phase.valid() || !definition.valid() || uint64(e.StageIndex) > uint64(len(definition.stages)) {
		return ErrInvalidExecutionState
	}
	input, err := agent.ParseInput(e.CurrentValue)
	if err != nil {
		return fmt.Errorf("%w: current value: %w", ErrInvalidExecutionState, err)
	}
	if e.StageIndex < uint32(len(definition.stages)) {
		if err := definition.stages[e.StageIndex].inputSchema.ValidateInput(input); err != nil {
			return fmt.Errorf("%w: current value does not satisfy current Stage: %w", ErrInvalidExecutionState, err)
		}
	} else {
		output, err := agent.ParseOutput(e.CurrentValue)
		if err != nil || definition.descriptor.ValidateOutput(output) != nil {
			return fmt.Errorf("%w: final value", ErrInvalidExecutionState)
		}
	}
	return e.validatePhaseState(definition)
}

func (e executionState) validatePhaseState(definition *Definition) error {
	hasChild := e.ChildProcessID != nil && e.ChildProcessID.Valid()
	hasWait := e.WaitID != nil && e.WaitID.Valid()
	switch e.Phase {
	case phaseReady:
		if e.StageIndex >= uint32(len(definition.stages)) || !e.noProgress() {
			return ErrInvalidExecutionState
		}
	case phaseAwaitingChildStart:
		if !e.singleChildStage(definition) || e.ChildProcessID != nil || e.WaitID != nil || e.hasFanoutProgress() {
			return ErrInvalidExecutionState
		}
	case phaseAwaitingChildWaitOpen:
		if !e.singleChildStage(definition) || !hasChild || e.WaitID != nil || e.hasFanoutProgress() {
			return ErrInvalidExecutionState
		}
	case phaseWaitingChild:
		if !e.singleChildStage(definition) || !hasChild || !hasWait || e.hasFanoutProgress() {
			return ErrInvalidExecutionState
		}
	case phaseAwaitingFanoutStarts, phaseAwaitingFanoutWaitOpen, phaseWaitingFanout:
		if e.SelectedCaseID != "" || e.ChildProcessID != nil || e.LoopIteration != 0 {
			return ErrInvalidExecutionState
		}
		if err := e.validateFanout(definition); err != nil {
			return ErrInvalidExecutionState
		}
	case phaseCompleted:
		if e.StageIndex != uint32(len(definition.stages)) || !e.noProgress() {
			return ErrInvalidExecutionState
		}
	}
	return nil
}

func (e executionState) singleChildStage(definition *Definition) bool {
	if e.StageIndex >= uint32(len(definition.stages)) {
		return false
	}
	stage := definition.stages[e.StageIndex]
	switch stage.kind {
	case stageKindCall:
		return e.SelectedCaseID == "" && e.LoopIteration == 0
	case stageKindSwitch:
		_, found := stage.switcher.binding(e.SelectedCaseID)
		return found && e.LoopIteration == 0
	case stageKindLoop:
		return e.SelectedCaseID == "" && e.LoopIteration > 0 &&
			e.LoopIteration <= stage.loop.maxIterations
	default:
		return false
	}
}

func (e executionState) noProgress() bool {
	return e.SelectedCaseID == "" && e.ChildProcessID == nil && e.WaitID == nil &&
		e.NextFanoutIndex == 0 && e.ActiveFanoutWindow == nil && e.FanoutOutputs == nil &&
		e.LoopIteration == 0
}

func (e executionState) hasFanoutProgress() bool {
	return e.NextFanoutIndex != 0 || e.ActiveFanoutWindow != nil || e.FanoutOutputs != nil
}

func (e executionState) validateFanout(definition *Definition) error {
	stage, windowStart, err := e.validateFanoutBoundary(definition)
	if err != nil {
		return err
	}
	resolved, started, err := e.validateFanoutChildren(windowStart)
	if err != nil {
		return err
	}
	if err := e.validateFanoutOutputs(stage, windowStart); err != nil {
		return err
	}
	return e.validateFanoutPhase(resolved, started)
}

func (e executionState) validateFanoutBoundary(definition *Definition) (Stage, uint32, error) {
	if e.StageIndex >= uint32(len(definition.stages)) {
		return Stage{}, 0, ErrInvalidExecutionState
	}
	stage := definition.stages[e.StageIndex]
	count, err := stage.fanoutCount(e.CurrentValue)
	if err != nil || count == 0 || e.NextFanoutIndex == 0 || e.NextFanoutIndex > count ||
		len(e.ActiveFanoutWindow) == 0 || uint64(len(e.ActiveFanoutWindow)) > uint64(stage.fanoutWindowSize()) ||
		uint64(len(e.ActiveFanoutWindow)) > uint64(e.NextFanoutIndex) ||
		uint64(len(e.FanoutOutputs)) != uint64(count) {
		return Stage{}, 0, ErrInvalidExecutionState
	}
	return stage, e.NextFanoutIndex - uint32(len(e.ActiveFanoutWindow)), nil
}

func (e executionState) validateFanoutChildren(windowStart uint32) (int, int, error) {
	resolved := 0
	started := make(map[agent.ProcessID]struct{}, len(e.ActiveFanoutWindow))
	for offset, child := range e.ActiveFanoutWindow {
		if child.FanoutIndex != windowStart+uint32(offset) {
			return 0, 0, ErrInvalidExecutionState
		}
		hasProcess := child.ChildProcessID != nil && child.ChildProcessID.Valid()
		hasFailure := child.Failure != nil && child.Failure.Valid()
		if child.ChildProcessID != nil && !hasProcess || child.Failure != nil && !hasFailure {
			return 0, 0, ErrInvalidExecutionState
		}
		if hasProcess || hasFailure {
			resolved++
		}
		if hasProcess {
			if _, duplicate := started[*child.ChildProcessID]; duplicate {
				return 0, 0, ErrInvalidExecutionState
			}
			started[*child.ChildProcessID] = struct{}{}
		}
	}
	if resolved != 0 && resolved != len(e.ActiveFanoutWindow) {
		return 0, 0, ErrInvalidExecutionState
	}
	return resolved, len(started), nil
}

func (e executionState) validateFanoutOutputs(stage Stage, windowStart uint32) error {
	for index, output := range e.FanoutOutputs {
		if uint32(index) < windowStart {
			if output == nil {
				return ErrInvalidExecutionState
			}
			value, err := agent.ParseOutput(*output)
			if err != nil {
				return ErrInvalidExecutionState
			}
			if err := stage.fanoutOutputSchema().ValidateOutput(value); err != nil {
				return ErrInvalidExecutionState
			}
		} else if output != nil {
			return ErrInvalidExecutionState
		}
	}
	return nil
}

func (e executionState) validateFanoutPhase(resolved, started int) error {
	switch e.Phase {
	case phaseAwaitingFanoutStarts:
		if e.WaitID != nil || resolved != 0 && started != 0 {
			return ErrInvalidExecutionState
		}
	case phaseAwaitingFanoutWaitOpen:
		if e.WaitID != nil || resolved != len(e.ActiveFanoutWindow) || started == 0 {
			return ErrInvalidExecutionState
		}
		for _, child := range e.ActiveFanoutWindow {
			if child.ChildProcessID != nil && child.Failure != nil {
				return ErrInvalidExecutionState
			}
		}
	case phaseWaitingFanout:
		if e.WaitID == nil || !e.WaitID.Valid() || resolved != len(e.ActiveFanoutWindow) || started == 0 {
			return ErrInvalidExecutionState
		}
	default:
		return ErrInvalidExecutionState
	}
	return nil
}
