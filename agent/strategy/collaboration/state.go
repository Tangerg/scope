package collaboration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

type phase string

const (
	phaseReady        phase = "ready"
	phaseStartingTurn phase = "starting_turn"
	phaseApplying     phase = "applying"
	phaseOpening      phase = "opening"
	phaseWaiting      phase = "waiting"
	phaseCompleted    phase = "completed"
	phaseFailed       phase = "failed"
)

type turnExecution struct {
	Input   Turn                    `json:"input"`
	Start   *agent.ChildStartResult `json:"start,omitempty"`
	Outcome *agent.ChildOutcome     `json:"outcome,omitempty"`
}

func (t turnExecution) unresolved() bool {
	if t.Outcome == nil {
		return false
	}
	effects, known := t.Outcome.SubtreeUnresolvedEffects()
	return !known || len(effects) != 0
}

func (t turnExecution) failure() (agent.Failure, bool) {
	if t.Outcome != nil {
		return t.Outcome.Result().Termination().Failure()
	}
	if t.Start != nil {
		return t.Start.Failure()
	}
	return agent.Failure{}, false
}

type executionState struct {
	Phase        phase            `json:"phase"`
	Number       uint32           `json:"number"`
	State        agent.Input      `json:"state"`
	Tasks        []Task           `json:"tasks,omitempty"`
	Controls     []ControlReceipt `json:"controls,omitempty"`
	Turn         *turnExecution   `json:"turn,omitempty"`
	Mode         Mode             `json:"mode,omitempty"`
	WaitSequence uint64           `json:"wait_sequence"`
	WaitID       *agent.WaitID    `json:"wait_id,omitempty"`
	Output       *agent.Output    `json:"output,omitempty"`
}

func (e executionState) task(key agent.ChildKey) *Task {
	for index := range e.Tasks {
		if e.Tasks[index].Request.Key == key {
			return &e.Tasks[index]
		}
	}
	return nil
}

func (e executionState) remaining() []agent.ProcessID {
	var ids []agent.ProcessID
	for _, task := range e.Tasks {
		if task.Start == nil || task.Outcome != nil {
			continue
		}
		if id, present := task.Start.ProcessID(); present {
			ids = append(ids, id)
		}
	}
	if e.Turn != nil && e.Turn.Start != nil && e.Turn.Outcome == nil {
		if id, present := e.Turn.Start.ProcessID(); present {
			ids = append(ids, id)
		}
	}
	return ids
}

func (e executionState) waitSpec() (agent.ChildWaitSpec, error) {
	key, err := agent.ParseWaitKey(fmt.Sprintf("collaboration.wait.%d", e.WaitSequence))
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	spec := agent.ChildWaitSpec{Key: key, Children: e.remaining(), Boundary: agent.ChildWaitBoundaryDrained, Condition: agent.AnyChild()}
	if e.WaitSequence == 0 || !spec.Valid() {
		return agent.ChildWaitSpec{}, fmt.Errorf("%w: wait requires a sequence and outstanding children", ErrInvalidState)
	}
	return spec, nil
}

func matchesOutcome(start *agent.ChildStartResult, outcome *agent.ChildOutcome) bool {
	if outcome == nil {
		return true
	}
	if start == nil || !outcome.Valid() {
		return false
	}
	id, present := start.ProcessID()
	return present && childcall.OutcomeMatches(*outcome, start.Key(), id)
}

func (e *executionState) recordOutcome(outcome agent.ChildOutcome) bool {
	if e.Turn.Start != nil && e.Turn.Start.Key() == outcome.Key() {
		if e.Turn.Outcome != nil || !matchesOutcome(e.Turn.Start, &outcome) {
			return false
		}
		e.Turn.Outcome = &outcome
		return true
	}
	task := e.task(outcome.Key())
	if task == nil || task.Outcome != nil || !matchesOutcome(task.Start, &outcome) {
		return false
	}
	task.Outcome = &outcome
	return true
}

func (e executionState) validate(d *Definition) error {
	if err := d.descriptor.ValidateInput(e.State); err != nil {
		return fmt.Errorf("%w: state: %w", ErrInvalidState, err)
	}
	if e.Number > d.maxTurns || uint64(len(e.Tasks)) > uint64(d.maxTasks) || uint64(len(e.Controls)) > uint64(d.maxControlsPerTurn) {
		return fmt.Errorf("%w: turn, task, or control bound exceeded", ErrInvalidState)
	}
	if e.WaitSequence > uint64(e.Number)+uint64(len(e.Tasks)) {
		return fmt.Errorf("%w: wait sequence exceeds declared turns and tasks", ErrInvalidState)
	}
	pending, active := 0, 0
	var ids []agent.ProcessID
	for index, task := range e.Tasks {
		if err := d.validateRequest(task.Request); err != nil {
			return fmt.Errorf("%w: task %d request: %w", ErrInvalidState, index, err)
		}
		for _, previous := range e.Tasks[:index] {
			if previous.Request.Key == task.Request.Key {
				return fmt.Errorf("%w: task %d duplicates key %q", ErrInvalidState, index, task.Request.Key)
			}
		}
		worker, _ := d.worker(task.Request.Worker)
		if task.Start == nil {
			pending++
		} else {
			if pending > 0 {
				return fmt.Errorf("%w: task %d start follows a pending start", ErrInvalidState, index)
			}
			if !task.Start.Valid() || !childcall.StartMatches(*task.Start, task.Request.Key, worker.deploymentRef) {
				return fmt.Errorf("%w: task %d start does not match its request", ErrInvalidState, index)
			}
			if id, present := task.Start.ProcessID(); present {
				if slices.Contains(ids, id) {
					return fmt.Errorf("%w: task %d reuses process %q", ErrInvalidState, index, id)
				}
				ids = append(ids, id)
				if task.Outcome == nil {
					active++
				}
			}
		}
		if !matchesOutcome(task.Start, task.Outcome) {
			return fmt.Errorf("%w: task %d outcome does not match its start", ErrInvalidState, index)
		}
		if task.Outcome != nil {
			if output, completed := task.Outcome.Result().Output(); completed {
				if err := worker.descriptor.ValidateOutput(output); err != nil {
					return fmt.Errorf("%w: task %d output: %w", ErrInvalidState, index, err)
				}
			}
		}
	}
	if uint64(active+pending) > uint64(d.maxConcurrentTasks) {
		return fmt.Errorf("%w: active and pending tasks exceed concurrency bound", ErrInvalidState)
	}
	pendingControls := 0
	for index, receipt := range e.Controls {
		effect, err := e.controlEffect(receipt.Control)
		if err != nil {
			return fmt.Errorf("%w: control %d: %w", ErrInvalidState, index, err)
		}
		if receipt.Result == nil {
			pendingControls++
		} else if pending > 0 || pendingControls > 0 {
			return fmt.Errorf("%w: control %d result precedes pending work", ErrInvalidState, index)
		} else if !receipt.Result.Matches(effect) {
			return fmt.Errorf("%w: control %d result does not match its effect", ErrInvalidState, index)
		}
	}
	if e.Phase == phaseReady {
		if e.Number != 0 || len(e.Tasks)+len(e.Controls) != 0 || e.Turn != nil || e.Mode != "" || e.WaitSequence != 0 || e.WaitID != nil || e.Output != nil {
			return fmt.Errorf("%w: ready phase retains execution progress", ErrInvalidState)
		}
		return nil
	}
	if err := e.validateTurn(d, ids); err != nil {
		return err
	}
	if e.Phase != phaseApplying && pending+pendingControls != 0 {
		return fmt.Errorf("%w: phase %q retains unapplied work", ErrInvalidState, e.Phase)
	}
	if (e.Phase == phaseWaiting) != (e.WaitID != nil) || e.WaitID != nil && !e.WaitID.Valid() {
		return fmt.Errorf("%w: wait identity does not match phase %q", ErrInvalidState, e.Phase)
	}
	if (e.Phase == phaseCompleted) != (e.Output != nil) {
		return fmt.Errorf("%w: output does not match phase %q", ErrInvalidState, e.Phase)
	}
	switch e.Phase {
	case phaseStartingTurn:
		if e.Turn.Start != nil || e.Turn.Outcome != nil || e.Mode != "" {
			return fmt.Errorf("%w: starting turn retains a start, outcome, or decision", ErrInvalidState)
		}
	case phaseApplying:
		if e.Turn.Outcome == nil || pending+pendingControls == 0 || e.Mode != Continue && e.Mode != Wait {
			return fmt.Errorf("%w: applying phase requires a continuing decision and pending work", ErrInvalidState)
		}
	case phaseOpening, phaseWaiting:
		if e.Turn.Start == nil {
			return fmt.Errorf("%w: waiting requires a turn start", ErrInvalidState)
		}
		if e.Mode == Wait && e.hasUnseenOutcome() {
			return fmt.Errorf("%w: waiting decision has unseen task outcomes", ErrInvalidState)
		}
		if e.Turn.Outcome == nil && e.Mode != "" || e.Turn.Outcome != nil && e.Mode != Wait {
			return fmt.Errorf("%w: waiting mode contradicts turn outcome", ErrInvalidState)
		}
		if _, err := e.waitSpec(); err != nil {
			return fmt.Errorf("%w: child wait: %w", ErrInvalidState, err)
		}
	case phaseCompleted:
		if e.Turn.Outcome == nil || e.Mode != Complete {
			return fmt.Errorf("%w: completed phase requires a completed turn decision", ErrInvalidState)
		}
		if err := d.descriptor.ValidateOutput(*e.Output); err != nil {
			return fmt.Errorf("%w: completed output: %w", ErrInvalidState, err)
		}
	case phaseFailed:
		if _, failed := e.Turn.failure(); !failed && !e.Turn.unresolved() || e.Mode != "" {
			return fmt.Errorf("%w: failed phase requires a failed or unresolved turn without a decision", ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unknown phase %q", ErrInvalidState, e.Phase)
	}
	return nil
}

func (e executionState) validateTurn(d *Definition, ids []agent.ProcessID) error {
	if e.Turn == nil || e.Number == 0 || e.Turn.Input.Number != e.Number {
		return fmt.Errorf("%w: turn is missing or its number does not match", ErrInvalidState)
	}
	if len(e.Turn.Input.Tasks) > len(e.Tasks) {
		return fmt.Errorf("%w: turn input contains undeclared tasks", ErrInvalidState)
	}
	if err := d.descriptor.ValidateInput(e.Turn.Input.State); err != nil {
		return fmt.Errorf("%w: turn input state: %w", ErrInvalidState, err)
	}
	var descriptors []agent.Descriptor
	for _, worker := range d.workers {
		descriptors = append(descriptors, worker.descriptor)
	}
	if !sameJSON(descriptors, e.Turn.Input.Workers) {
		return fmt.Errorf("%w: turn workers do not match the definition", ErrInvalidState)
	}
	for index, task := range e.Turn.Input.Tasks {
		current := e.Tasks[index]
		if !sameJSON(task.Request, current.Request) || task.Start == nil || !sameJSON(task.Start, current.Start) ||
			task.Outcome != nil && !sameJSON(task.Outcome, current.Outcome) {
			return fmt.Errorf("%w: turn task %d does not match current task evidence", ErrInvalidState, index)
		}
	}
	if uint64(len(e.Turn.Input.Controls)) > uint64(d.maxControlsPerTurn) {
		return fmt.Errorf("%w: turn controls exceed the per-turn bound", ErrInvalidState)
	}
	captured := executionState{Tasks: e.Turn.Input.Tasks}
	for index, receipt := range e.Turn.Input.Controls {
		effect, err := captured.controlEffect(receipt.Control)
		if err != nil {
			return fmt.Errorf("%w: turn control %d: %w", ErrInvalidState, index, err)
		}
		if receipt.Result == nil || !receipt.Result.Matches(effect) {
			return fmt.Errorf("%w: turn control %d has no matching result", ErrInvalidState, index)
		}
	}
	if e.Turn.Start != nil {
		key, err := turnKey(e.Number)
		if err != nil {
			return fmt.Errorf("%w: turn key: %w", ErrInvalidState, err)
		}
		if !e.Turn.Start.Valid() || !childcall.StartMatches(*e.Turn.Start, key, d.coordinator.deploymentRef) {
			return fmt.Errorf("%w: turn start does not match the coordinator request", ErrInvalidState)
		}
		id, present := e.Turn.Start.ProcessID()
		if !present && e.Phase != phaseFailed || slices.Contains(ids, id) {
			return fmt.Errorf("%w: turn process is absent or reused by a task", ErrInvalidState)
		}
	}
	if !matchesOutcome(e.Turn.Start, e.Turn.Outcome) {
		return fmt.Errorf("%w: turn outcome does not match its start", ErrInvalidState)
	}
	if e.Mode == "" {
		if e.Turn.Outcome != nil && e.Phase != phaseFailed || len(e.Tasks) != len(e.Turn.Input.Tasks) ||
			!sameJSON(e.State, e.Turn.Input.State) || !sameJSON(e.Controls, nilIfEmpty(e.Turn.Input.Controls)) {
			return fmt.Errorf("%w: turn without a decision changed state, tasks, or controls", ErrInvalidState)
		}
		return nil
	}
	return e.validateAppliedDecision(d)
}

func (e executionState) validateAppliedDecision(d *Definition) error {
	if e.Turn.Outcome == nil || e.Turn.unresolved() {
		return fmt.Errorf("%w: applied decision requires a resolved turn outcome", ErrInvalidState)
	}
	result := e.Turn.Outcome.Result()
	output, present := result.Output()
	if !present || result.Status() != agent.StatusCompleted {
		return fmt.Errorf("%w: applied decision requires a completed turn output", ErrInvalidState)
	}
	decision, err := d.coordinator.descriptor.DecodeOutput[Decision](output)
	if err != nil {
		return fmt.Errorf("%w: coordinator output: %w", ErrInvalidState, err)
	}
	previousCount := len(e.Turn.Input.Tasks)
	if len(e.Tasks) != previousCount+len(decision.Tasks) || len(e.Controls) != len(decision.Controls) ||
		e.Mode != decision.Mode || !sameJSON(e.State, decision.State) || !sameJSON(e.Output, decision.Output) {
		return fmt.Errorf("%w: applied state does not match the coordinator decision", ErrInvalidState)
	}
	for index, request := range decision.Tasks {
		if !sameJSON(request, e.Tasks[previousCount+index].Request) {
			return fmt.Errorf("%w: applied task %d does not match the coordinator decision", ErrInvalidState, index)
		}
	}
	for index, control := range decision.Controls {
		if !sameJSON(control, e.Controls[index].Control) {
			return fmt.Errorf("%w: applied control %d does not match the coordinator decision", ErrInvalidState, index)
		}
	}
	before := e
	before.Tasks = e.Tasks[:previousCount]
	if err := before.validateDecision(d, decision); err != nil {
		return fmt.Errorf("%w: applied decision: %w", ErrInvalidState, err)
	}
	return nil
}

func (e executionState) validateDecision(definition *Definition, decision Decision) error {
	if err := definition.descriptor.ValidateInput(decision.State); err != nil {
		return fmt.Errorf("%w: state: %w", ErrInvalidDecision, err)
	}
	if decision.Mode == Complete {
		if decision.Output == nil || len(decision.Tasks) != 0 || len(decision.Controls) != 0 {
			return ErrInvalidDecision
		}
		if err := definition.descriptor.ValidateOutput(*decision.Output); err != nil {
			return fmt.Errorf("%w: output: %w", ErrInvalidDecision, err)
		}
		return nil
	}
	if decision.Mode != Continue && decision.Mode != Wait || decision.Output != nil {
		return ErrInvalidDecision
	}
	if uint64(len(e.Tasks))+uint64(len(decision.Tasks)) > uint64(definition.maxTasks) ||
		uint64(len(e.remaining()))+uint64(len(decision.Tasks)) > uint64(definition.maxConcurrentTasks) ||
		uint64(len(decision.Controls)) > uint64(definition.maxControlsPerTurn) {
		return fmt.Errorf("%w: task or control bound exceeded", ErrInvalidDecision)
	}
	for index, request := range decision.Tasks {
		if err := definition.validateRequest(request); err != nil {
			return err
		}
		if e.task(request.Key) != nil {
			return fmt.Errorf("%w: reused task key", ErrInvalidDecision)
		}
		for _, previous := range decision.Tasks[:index] {
			if previous.Key == request.Key {
				return fmt.Errorf("%w: duplicate task key", ErrInvalidDecision)
			}
		}
	}
	for _, control := range decision.Controls {
		if _, err := e.controlEffect(control); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidDecision, err)
		}
	}
	if decision.Mode == Wait && len(e.remaining())+len(decision.Tasks) == 0 && !e.hasUnseenOutcome() {
		return fmt.Errorf("%w: wait has no outstanding tasks", ErrInvalidDecision)
	}
	return nil
}

func (e executionState) controlEffect(control Control) (agent.Effect, error) {
	task := e.task(control.Task)
	if task == nil || task.Start == nil || (control.Signal == nil) == (control.CancelReason == nil) {
		return agent.Effect{}, ErrInvalidDecision
	}
	id, started := task.Start.ProcessID()
	if !started {
		return agent.Effect{}, ErrInvalidDecision
	}
	if control.Signal != nil {
		return agent.SignalChild(id, *control.Signal)
	}
	return agent.CancelChild(id, *control.CancelReason)
}

func (e executionState) hasUnseenOutcome() bool {
	if e.Turn == nil {
		return false
	}
	for index, task := range e.Tasks {
		if task.Outcome != nil && (index >= len(e.Turn.Input.Tasks) || e.Turn.Input.Tasks[index].Outcome == nil) {
			return true
		}
	}
	return false
}

func nilIfEmpty[T any](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	return values
}

func sameJSON(left, right any) bool {
	first, err := json.Marshal(left)
	if err != nil {
		return false
	}
	second, err := json.Marshal(right)
	return err == nil && bytes.Equal(first, second)
}

const turnPrefix = "collaboration.turn."

func turnKey(number uint32) (agent.ChildKey, error) {
	return agent.ParseChildKey(fmt.Sprintf("%s%d", turnPrefix, number))
}
