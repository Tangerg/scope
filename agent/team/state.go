package team

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/agent/internal/strategy/child"
	run "github.com/Tangerg/scope/agent/run"
)

const turnPrefix = "collaboration.turn."

func turnKey(number uint64) (run.ChildKey, error) {
	return run.ParseChildKey(fmt.Sprintf("%s%d", turnPrefix, number))
}

type taskState struct {
	Request    TaskRequest      `json:"request"`
	Invocation child.Invocation `json:"invocation"`
}

func (t taskState) validate(definition *definition) error {
	if err := definition.validateRequest(t.Request); err != nil {
		return err
	}
	if !t.Invocation.Valid() {
		return ErrInvalidExecutionState
	}
	if t.Invocation.Process != nil {
		if key, _ := t.Invocation.Process.Key(); key != t.Request.Key {
			return ErrInvalidExecutionState
		}
	}
	if t.Invocation.Termination == nil {
		return nil
	}
	output, completed := t.Invocation.Termination.Output()
	if !completed {
		return nil
	}
	worker, _ := definition.worker(t.Request.Worker)
	return worker.program.Descriptor().ValidateOutput(output)
}

func (t taskState) project() Task {
	return Task{Request: t.Request, Process: t.Invocation.Process, Outcome: t.Invocation.Termination, Rejection: t.Invocation.Rejection}
}

type controlState struct {
	Control Control         `json:"control"`
	Effect  *run.EffectID   `json:"effect,omitzero"`
	Result  *run.Settlement `json:"result,omitzero"`
}

// valid requires exactly one of a pending Effect and its definite result.
func (c controlState) valid() bool {
	if c.Control.validate() != nil || (c.Effect == nil) == (c.Result == nil) {
		return false
	}
	if c.Effect != nil {
		return c.Effect.Valid()
	}
	return c.Result.Valid() && c.Result.Status() != run.SettlementStatusUnknown
}

func (c controlState) project() ControlReceipt {
	return ControlReceipt{Control: c.Control, Result: c.Result}
}

type executionState struct {
	WorkingState   run.Payload       `json:"working_state,omitzero"`
	TurnNumber     uint64            `json:"turn_number"`
	Coordinator    *child.Invocation `json:"coordinator,omitzero"`
	Tasks          []taskState       `json:"tasks,omitempty"`
	Controls       []controlState    `json:"controls,omitempty"`
	WaitForTask    bool              `json:"wait_for_task,omitzero"`
	OutcomeArrived bool              `json:"outcome_arrived,omitzero"`
	Completed      bool              `json:"completed,omitzero"`
}

func (e *executionState) UnmarshalJSON(data []byte) error {
	type wire executionState
	value, err := jsonwire.Decode[wire](data)
	if err != nil {
		return err
	}
	*e = executionState(value)
	return nil
}
func (e executionState) remainingCount() int {
	count := 0
	for _, task := range e.Tasks {
		if !task.Invocation.Done() {
			count++
		}
	}
	return count
}
func (e executionState) unapplied() bool {
	for _, task := range e.Tasks {
		if task.Invocation.Request != nil {
			return true
		}
	}
	for _, control := range e.Controls {
		if control.Effect != nil {
			return true
		}
	}
	return false
}
func (e executionState) taskIndex() map[run.ChildKey]*Task {
	tasks := make(map[run.ChildKey]*Task, len(e.Tasks))
	for _, task := range e.Tasks {
		tasks[task.Request.Key] = new(task.project())
	}
	return tasks
}
func (e executionState) validate(ctx context.Context, definition *definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.Completed {
		return e.validateCompleted()
	}
	if err := definition.descriptor.ValidateInput(e.WorkingState); err != nil {
		return err
	}
	if !e.withinBounds(definition) || !e.coordinatorValid() {
		return ErrInvalidExecutionState
	}
	keys := make(map[run.ChildKey]struct{}, len(e.Tasks))
	for _, task := range e.Tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, exists := keys[task.Request.Key]; exists {
			return ErrInvalidExecutionState
		}
		if err := task.validate(definition); err != nil {
			return err
		}
		keys[task.Request.Key] = struct{}{}
	}
	for _, control := range e.Controls {
		if _, present := keys[control.Control.Task]; !present || !control.valid() {
			return ErrInvalidExecutionState
		}
	}
	return nil
}

// A completed collaboration keeps no state: its output belongs to the runtime.
func (e executionState) validateCompleted() error {
	if e.WorkingState.Valid() || e.Coordinator != nil || e.TurnNumber != 0 || len(e.Tasks)+len(e.Controls) != 0 || e.WaitForTask || e.OutcomeArrived {
		return ErrInvalidExecutionState
	}
	return nil
}

func (e executionState) withinBounds(definition *definition) bool {
	return definition.maxTurns.Allows(e.TurnNumber) && definition.maxTasks.Allows(uint64(len(e.Tasks))) &&
		len(e.Controls) <= int(definition.maxControlsPerTurn) && e.remainingCount() <= int(definition.maxConcurrentTasks)
}

// coordinatorValid checks that an active coordinator belongs to the current
// turn, whose number names its child key.
func (e executionState) coordinatorValid() bool {
	if e.Coordinator == nil {
		return true
	}
	if !e.Coordinator.Valid() || e.Coordinator.Done() || e.TurnNumber == 0 || e.WaitForTask {
		return false
	}
	if e.Coordinator.Process == nil {
		return true
	}
	expected, err := turnKey(e.TurnNumber)
	key, _ := e.Coordinator.Process.Key()
	return err == nil && expected == key
}

func (e executionState) validateDecision(ctx context.Context, definition *definition, decision Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := decision.validateShape(); err != nil {
		return err
	}
	if err := definition.descriptor.ValidateInput(decision.State); err != nil {
		return fmt.Errorf("%w: working state: %w", ErrInvalidDecision, err)
	}
	if decision.completes() {
		return definition.descriptor.ValidateOutput(decision.Output)
	}
	remaining := e.remainingCount()
	if !definition.maxTasks.Allows(uint64(len(e.Tasks)), uint64(len(decision.Tasks))) || uint64(remaining)+uint64(len(decision.Tasks)) > uint64(definition.maxConcurrentTasks) || len(decision.Controls) > int(definition.maxControlsPerTurn) {
		return ErrInvalidDecision
	}
	tasks := e.taskIndex()
	keys := make(map[run.ChildKey]struct{}, len(decision.Tasks))
	for _, request := range decision.Tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := definition.validateRequest(request); err != nil {
			return err
		}
		if tasks[request.Key] != nil {
			return fmt.Errorf("%w: reused task key", ErrInvalidDecision)
		}
		if _, exists := keys[request.Key]; exists {
			return fmt.Errorf("%w: duplicate task key", ErrInvalidDecision)
		}
		keys[request.Key] = struct{}{}
	}
	for _, control := range decision.Controls {
		if _, err := control.effect(tasks[control.Task]); err != nil {
			return err
		}
	}
	if decision.Mode == ModeWait && remaining+len(decision.Tasks) == 0 && !e.OutcomeArrived {
		return fmt.Errorf("%w: wait has no outstanding tasks", ErrInvalidDecision)
	}
	return nil
}
