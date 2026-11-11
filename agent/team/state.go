package team

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

const turnPrefix = "team.turn."

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
	return worker.Program.Descriptor().ValidateOutput(output)
}

func (t taskState) project() Task {
	return Task{Request: t.Request, Process: t.Invocation.Process, Outcome: t.Invocation.Termination, Rejection: t.Invocation.Rejection}
}

func (t taskState) processID(parent run.ProcessID) (run.ProcessID, bool) {
	if t.Invocation.Process == nil && t.Invocation.Termination == nil {
		return run.ProcessID{}, false
	}
	id, err := parent.Child(t.Request.Key)
	if err != nil || t.Invocation.Process != nil && *t.Invocation.Process != id {
		return run.ProcessID{}, false
	}
	return id, true
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

// nextTurnDue reports whether the coordinator runs again: every action of the
// last decision has its receipt, and a waiting decision has seen a task finish
// or has no task left to wait for.
func (e executionState) nextTurnDue() bool {
	waiting := e.WaitForTask && !e.OutcomeArrived && e.remainingCount() > 0
	return !e.unapplied() && !waiting
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

func (e executionState) taskIndex() map[run.ChildKey]*taskState {
	tasks := make(map[run.ChildKey]*taskState, len(e.Tasks))
	for index := range e.Tasks {
		task := &e.Tasks[index]
		tasks[task.Request.Key] = task
	}
	return tasks
}

func (e executionState) validate(ctx context.Context, definition *definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := definition.descriptor.ValidateInput(e.WorkingState); err != nil {
		return err
	}
	if !e.withinBounds(definition) || !e.coordinatorValid() {
		return ErrInvalidExecutionState
	}
	tasks := make(map[run.ChildKey]*taskState, len(e.Tasks))
	for index := range e.Tasks {
		task := &e.Tasks[index]
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, exists := tasks[task.Request.Key]; exists {
			return ErrInvalidExecutionState
		}
		if err := task.validate(definition); err != nil {
			return err
		}
		tasks[task.Request.Key] = task
	}
	for _, control := range e.Controls {
		task := tasks[control.Control.Task]
		if task == nil || !control.valid() || task.Invocation.Request != nil || task.Invocation.Rejection != nil {
			return ErrInvalidExecutionState
		}
	}
	return e.validateObligations(ctx)
}

func (e executionState) validateObligations(ctx context.Context) error {
	var claims obligationClaims
	if e.Coordinator != nil && !claims.claimInvocation(*e.Coordinator) {
		return ErrInvalidExecutionState
	}
	for index, task := range e.Tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !claims.claimInvocation(task.Invocation) {
			return ErrInvalidExecutionState
		}
		if request := task.Invocation.Request; request != nil {
			if uint64(request.Index) > uint64(index) || !claims.claimBatchStart(uint64(index)-uint64(request.Index)) {
				return ErrInvalidExecutionState
			}
		}
	}
	for index, control := range e.Controls {
		if err := ctx.Err(); err != nil {
			return err
		}
		if control.Effect == nil {
			continue
		}
		if !claims.claimEffect(*control.Effect) || uint64(control.Effect.Index) < uint64(index) {
			return ErrInvalidExecutionState
		}
		newTasks := uint64(control.Effect.Index) - uint64(index)
		if newTasks > uint64(len(e.Tasks)) || !claims.claimBatchStart(uint64(len(e.Tasks))-newTasks) {
			return ErrInvalidExecutionState
		}
	}
	return ctx.Err()
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
	if !e.Coordinator.Valid() || e.Coordinator.Done() || e.TurnNumber == 0 || e.WaitForTask || e.unapplied() {
		return false
	}
	if e.Coordinator.Request != nil && e.Coordinator.Request.Index != 0 {
		return false
	}
	if e.Coordinator.Process == nil {
		return true
	}
	expected, err := turnKey(e.TurnNumber)
	key, _ := e.Coordinator.Process.Key()
	return err == nil && expected == key
}

func (e executionState) validateDecision(ctx context.Context, definition *definition, process run.ProcessID, decision Decision) error {
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
		if _, err := control.effect(process, tasks[control.Task]); err != nil {
			return err
		}
	}
	if decision.Mode == ModeWait && remaining+len(decision.Tasks) == 0 && !e.OutcomeArrived {
		return fmt.Errorf("%w: wait has no outstanding tasks", ErrInvalidDecision)
	}
	return nil
}
