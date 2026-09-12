package workflow

import (
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
	Child                  *childcall.Single  `json:"child,omitempty"`
	FanoutWaitID           *agent.WaitID      `json:"fanout_wait_id,omitempty"`
	ActiveFanoutWindow     []fanoutChildState `json:"active_fanout_window,omitempty"`
	CompletedFanoutOutputs []json.RawMessage  `json:"completed_fanout_outputs,omitempty"`
	LoopIteration          uint32             `json:"loop_iteration,omitempty"`
}

type fanoutChildState struct {
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
		if err != nil {
			return fmt.Errorf("%w: final value: %w", ErrInvalidExecutionState, err)
		}
		if err := definition.descriptor.ValidateOutput(output); err != nil {
			return fmt.Errorf("%w: final value schema: %w", ErrInvalidExecutionState, err)
		}
	}
	return e.validatePhaseState(definition)
}

func (e executionState) validatePhaseState(definition *Definition) error {
	switch e.Phase {
	case phaseReady:
		if e.StageIndex >= uint32(len(definition.stages)) || !e.noProgress() {
			return ErrInvalidExecutionState
		}
	case phaseChild:
		if !e.singleChildStage(definition) || e.Child == nil || e.hasFanoutProgress() {
			return ErrInvalidExecutionState
		}
	case phaseAwaitingFanoutStarts, phaseAwaitingFanoutWaitOpen, phaseWaitingFanout:
		if e.SelectedCaseID != "" || e.Child != nil || e.LoopIteration != 0 {
			return ErrInvalidExecutionState
		}
		if err := e.validateFanout(definition); err != nil {
			return err
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
	case StageKindCall:
		return e.SelectedCaseID == "" && e.LoopIteration == 0
	case StageKindSwitch:
		_, found := stage.switcher.binding(e.SelectedCaseID)
		return found && e.LoopIteration == 0
	case StageKindLoop:
		return e.SelectedCaseID == "" && e.LoopIteration > 0 &&
			e.LoopIteration <= stage.loop.maxIterations
	default:
		return false
	}
}

func (e executionState) noProgress() bool {
	return e.SelectedCaseID == "" && e.Child == nil && e.FanoutWaitID == nil &&
		e.ActiveFanoutWindow == nil && e.CompletedFanoutOutputs == nil &&
		e.LoopIteration == 0
}

func (e executionState) hasFanoutProgress() bool {
	return e.FanoutWaitID != nil || e.ActiveFanoutWindow != nil || e.CompletedFanoutOutputs != nil
}

func (e executionState) validateFanout(definition *Definition) error {
	stage, err := e.validateFanoutBoundary(definition)
	if err != nil {
		return err
	}
	resolved, started, err := e.validateFanoutChildren()
	if err != nil {
		return err
	}
	if err := e.validateCompletedFanoutOutputs(stage); err != nil {
		return err
	}
	return e.validateFanoutPhase(resolved, started)
}

func (e executionState) fanoutWindowStart() uint32 {
	return uint32(len(e.CompletedFanoutOutputs))
}

func (e executionState) validateFanoutBoundary(definition *Definition) (Stage, error) {
	if e.StageIndex >= uint32(len(definition.stages)) {
		return Stage{}, ErrInvalidExecutionState
	}
	stage := definition.stages[e.StageIndex]
	if stage.kind != StageKindFork && stage.kind != StageKindMap {
		return Stage{}, ErrInvalidExecutionState
	}
	count, err := stage.fanout.source.count(e.CurrentValue)
	windowSize := stage.fanout.windowSize
	if err != nil {
		return Stage{}, fmt.Errorf("%w: fan-out count: %w", ErrInvalidExecutionState, err)
	}
	if uint64(len(e.CompletedFanoutOutputs)) >= uint64(count) {
		return Stage{}, ErrInvalidExecutionState
	}
	start := e.fanoutWindowStart()
	if start%windowSize != 0 || uint64(len(e.ActiveFanoutWindow)) != uint64(min(windowSize, count-start)) {
		return Stage{}, ErrInvalidExecutionState
	}
	return stage, nil
}

func (e executionState) validateFanoutChildren() (int, int, error) {
	resolved := 0
	started := make(map[agent.ProcessID]struct{}, len(e.ActiveFanoutWindow))
	for _, child := range e.ActiveFanoutWindow {
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

func (e executionState) validateCompletedFanoutOutputs(stage Stage) error {
	for _, output := range e.CompletedFanoutOutputs {
		value, err := agent.ParseOutput(output)
		if err != nil {
			return fmt.Errorf("%w: completed fan-out output: %w", ErrInvalidExecutionState, err)
		}
		if err := stage.fanout.outputSchema.ValidateOutput(value); err != nil {
			return fmt.Errorf("%w: completed fan-out output schema: %w", ErrInvalidExecutionState, err)
		}
	}
	return nil
}

func (e executionState) validateFanoutPhase(resolved, started int) error {
	switch e.Phase {
	case phaseAwaitingFanoutStarts:
		if e.FanoutWaitID != nil || resolved != 0 && started != 0 {
			return ErrInvalidExecutionState
		}
	case phaseAwaitingFanoutWaitOpen:
		if e.FanoutWaitID != nil || resolved != len(e.ActiveFanoutWindow) || started == 0 {
			return ErrInvalidExecutionState
		}
		for _, child := range e.ActiveFanoutWindow {
			if child.ChildProcessID != nil && child.Failure != nil {
				return ErrInvalidExecutionState
			}
		}
	case phaseWaitingFanout:
		if e.FanoutWaitID == nil || !e.FanoutWaitID.Valid() || resolved != len(e.ActiveFanoutWindow) || started == 0 {
			return ErrInvalidExecutionState
		}
	default:
		return ErrInvalidExecutionState
	}
	return nil
}

func (e executionState) snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(e)
	if err != nil {
		return agent.ExecutionState{}, fmt.Errorf("workflow: encode execution state: %w", err)
	}
	return agent.NewExecutionState(executionStateKind, payload)
}
