package collaboration

import (
	"bytes"
	"context"
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
	Start   *agent.ChildStartResult `json:"start,omitzero"`
	Outcome *agent.ChildOutcome     `json:"outcome,omitzero"`
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
	Number       uint64           `json:"number"`
	State        agent.Payload    `json:"state"`
	Tasks        []Task           `json:"tasks,omitempty"`
	Controls     []ControlReceipt `json:"controls,omitempty"`
	Turn         *turnExecution   `json:"turn,omitzero"`
	Mode         Mode             `json:"mode,omitempty"`
	WaitSequence uint64           `json:"wait_sequence"`
	WaitID       *agent.WaitID    `json:"wait_id,omitzero"`
	Output       agent.Payload    `json:"output,omitzero"`
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

func (e executionState) batch(d *Definition) (childcall.Batch, error) {
	batch := childcall.Batch{Children: make([]childcall.Child, len(e.Tasks))}
	if e.WaitID != nil {
		batch.WaitID = *e.WaitID
	}
	for index, task := range e.Tasks {
		worker, _ := d.worker(task.Request.Worker)
		child := &batch.Children[index]
		child.Key, child.Deployment = task.Request.Key, worker.deploymentRef
		if task.Start != nil {
			id, started := task.Start.ProcessID()
			child.ProcessID, child.Done = id, !started || task.Outcome != nil
		}
	}
	if e.Turn != nil {
		key, err := turnKey(e.Number)
		if err != nil {
			return childcall.Batch{}, err
		}
		child := childcall.Child{Key: key, Deployment: d.coordinator.deploymentRef}
		if e.Turn.Start != nil {
			id, started := e.Turn.Start.ProcessID()
			child.ProcessID, child.Done = id, !started || e.Turn.Outcome != nil
		}
		batch.Children = append(batch.Children, child)
	}
	return batch, nil
}

func (e executionState) waitSpec(d *Definition) (agent.ChildWaitSpec, error) {
	key, err := agent.ParseWaitKey(fmt.Sprintf("collaboration.wait.%d", e.WaitSequence))
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	if e.WaitSequence == 0 {
		return agent.ChildWaitSpec{}, fmt.Errorf("%w: wait requires a sequence", ErrInvalidState)
	}
	batch, err := e.batch(d)
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	return batch.WaitSpec(key, agent.ChildWaitBoundaryDrained, agent.AnyChild())
}

func (e *executionState) recordStart(index int, start agent.ChildStartResult) {
	if index == len(e.Tasks) {
		e.Turn.Start = &start
		return
	}
	e.Tasks[index].Start = &start
}

func (e *executionState) recordOutcome(index int, outcome agent.ChildOutcome) {
	if index == len(e.Tasks) {
		e.Turn.Outcome = &outcome
		return
	}
	e.Tasks[index].Outcome = &outcome
}

func (e executionState) validateOutcomes(ctx context.Context, d *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	batch, err := e.batch(d)
	if err != nil {
		return err
	}
	batch.WaitID = agent.WaitID{}
	var outcomes []agent.ChildOutcome
	var expected []int
	for index, task := range e.Tasks {
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		if task.Outcome != nil {
			batch.Children[index].Done = false
			outcomes = append(outcomes, *task.Outcome)
			expected = append(expected, index)
		}
	}
	if e.Turn != nil && e.Turn.Outcome != nil {
		batch.Children[len(e.Tasks)].Done = false
		outcomes = append(outcomes, *e.Turn.Outcome)
		expected = append(expected, len(e.Tasks))
	}
	indices, err := batch.MatchOutcomes(outcomes)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidState, err)
	}
	if !slices.Equal(indices, expected) {
		return fmt.Errorf("%w: outcome belongs to another child", ErrInvalidState)
	}
	return ctx.Err()
}

func (e executionState) validate(ctx context.Context, d *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.descriptor.ValidateInput(e.State); err != nil {
		return fmt.Errorf("%w: state: %w", ErrInvalidState, err)
	}
	if !d.maxTurns.Allows(e.Number) || !d.maxTasks.Allows(uint64(len(e.Tasks))) || uint64(len(e.Controls)) > uint64(d.maxControlsPerTurn) {
		return fmt.Errorf("%w: turn, task, or control bound exceeded", ErrInvalidState)
	}
	if e.WaitSequence > e.Number && e.WaitSequence-e.Number > uint64(len(e.Tasks)) {
		return fmt.Errorf("%w: wait sequence exceeds declared turns and tasks", ErrInvalidState)
	}
	if err := e.validateOutcomes(ctx, d); err != nil {
		return err
	}
	pending, ids, err := e.validateTasks(ctx, d)
	if err != nil {
		return err
	}
	pendingControls, err := e.validateControls(ctx, pending)
	if err != nil {
		return err
	}
	if e.Phase == phaseReady {
		if e.Number != 0 || len(e.Tasks)+len(e.Controls) != 0 || e.Turn != nil || e.Mode != Undecided || e.WaitSequence != 0 || e.WaitID != nil || e.Output.Valid() {
			return fmt.Errorf("%w: ready phase retains execution progress", ErrInvalidState)
		}
		return nil
	}
	if err := e.validateTurn(ctx, d, ids); err != nil {
		return err
	}
	if e.Phase != phaseApplying && pending+pendingControls != 0 {
		return fmt.Errorf("%w: phase %q retains unapplied work", ErrInvalidState, e.Phase)
	}
	if (e.Phase == phaseWaiting) != (e.WaitID != nil) || e.WaitID != nil && !e.WaitID.Valid() {
		return fmt.Errorf("%w: wait identity does not match phase %q", ErrInvalidState, e.Phase)
	}
	if (e.Phase == phaseCompleted) != (e.Output.Valid()) {
		return fmt.Errorf("%w: output does not match phase %q", ErrInvalidState, e.Phase)
	}
	switch e.Phase {
	case phaseStartingTurn:
		if e.Turn.Start != nil || e.Turn.Outcome != nil || e.Mode != Undecided {
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
		if e.Turn.Outcome == nil && e.Mode != Undecided || e.Turn.Outcome != nil && e.Mode != Wait {
			return fmt.Errorf("%w: waiting mode contradicts turn outcome", ErrInvalidState)
		}
		if _, err := e.waitSpec(d); err != nil {
			return fmt.Errorf("%w: child wait: %w", ErrInvalidState, err)
		}
	case phaseCompleted:
		if e.Turn.Outcome == nil || e.Mode != Complete {
			return fmt.Errorf("%w: completed phase requires a completed turn decision", ErrInvalidState)
		}
		if err := d.descriptor.ValidateOutput(e.Output); err != nil {
			return fmt.Errorf("%w: completed output: %w", ErrInvalidState, err)
		}
	case phaseFailed:
		if _, failed := e.Turn.failure(); !failed && !e.Turn.unresolved() || e.Mode != Undecided {
			return fmt.Errorf("%w: failed phase requires a failed or unresolved turn without a decision", ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unknown phase %q", ErrInvalidState, e.Phase)
	}
	return ctx.Err()
}

func (e executionState) validateTasks(ctx context.Context, d *Definition) (int, map[agent.ProcessID]struct{}, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	pending, active := 0, 0
	ids := make(map[agent.ProcessID]struct{}, len(e.Tasks))
	keys := make(map[agent.ChildKey]struct{}, len(e.Tasks))
	for index, task := range e.Tasks {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		if err := d.validateRequest(task.Request); err != nil {
			return 0, nil, fmt.Errorf("%w: task %d request: %w", ErrInvalidState, index, err)
		}
		if _, duplicate := keys[task.Request.Key]; duplicate {
			return 0, nil, fmt.Errorf("%w: task %d duplicates key %q", ErrInvalidState, index, task.Request.Key)
		}
		keys[task.Request.Key] = struct{}{}
		worker, _ := d.worker(task.Request.Worker)
		if task.Start == nil {
			pending++
		} else {
			if pending > 0 {
				return 0, nil, fmt.Errorf("%w: task %d start follows a pending start", ErrInvalidState, index)
			}
			if !task.Start.Matches(task.Request.Key, worker.deploymentRef) {
				return 0, nil, fmt.Errorf("%w: task %d start does not match its request", ErrInvalidState, index)
			}
			if id, present := task.Start.ProcessID(); present {
				if _, duplicate := ids[id]; duplicate {
					return 0, nil, fmt.Errorf("%w: task %d reuses process %q", ErrInvalidState, index, id)
				}
				ids[id] = struct{}{}
				if task.Outcome == nil {
					active++
				}
			}
		}
		if task.Outcome != nil {
			if output, completed := task.Outcome.Result().Output(); completed {
				if err := worker.descriptor.ValidateOutput(output); err != nil {
					return 0, nil, fmt.Errorf("%w: task %d output: %w", ErrInvalidState, index, err)
				}
			}
		}
	}
	if uint64(active+pending) > uint64(d.maxConcurrentTasks) {
		return 0, nil, fmt.Errorf("%w: active and pending tasks exceed concurrency bound", ErrInvalidState)
	}
	return pending, ids, nil
}

func (e executionState) validateControls(ctx context.Context, pending int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	pendingControls := 0
	for index, receipt := range e.Controls {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		effect, err := e.controlEffect(receipt.Control)
		if err != nil {
			return 0, fmt.Errorf("%w: control %d: %w", ErrInvalidState, index, err)
		}
		if receipt.Result == nil {
			pendingControls++
		} else if pending > 0 || pendingControls > 0 {
			return 0, fmt.Errorf("%w: control %d result precedes pending work", ErrInvalidState, index)
		} else if !receipt.Result.Matches(effect) {
			return 0, fmt.Errorf("%w: control %d result does not match its effect", ErrInvalidState, index)
		}
	}
	return pendingControls, nil
}

func (e executionState) validateTurn(ctx context.Context, d *Definition, ids map[agent.ProcessID]struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.Turn == nil || e.Number == 0 || e.Turn.Input.Number != e.Number {
		return fmt.Errorf("%w: turn is missing or its number does not match", ErrInvalidState)
	}
	if len(e.Turn.Input.Tasks) > len(e.Tasks) {
		return fmt.Errorf("%w: turn input contains undeclared tasks", ErrInvalidState)
	}
	if err := d.descriptor.ValidateInput(e.Turn.Input.State); err != nil {
		return fmt.Errorf("%w: turn input state: %w", ErrInvalidState, err)
	}
	if len(e.Turn.Input.Workers) != len(d.workers) {
		return fmt.Errorf("%w: turn workers do not match the definition: count differs", ErrInvalidState)
	}
	for index, worker := range d.workers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Turn.Input.Workers[index].Digest() != worker.descriptor.Digest() {
			return fmt.Errorf("%w: turn worker %d does not match the definition", ErrInvalidState, index)
		}
	}
	for index, task := range e.Turn.Input.Tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		if err := ctx.Err(); err != nil {
			return err
		}
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
		if !e.Turn.Start.Matches(key, d.coordinator.deploymentRef) {
			return fmt.Errorf("%w: turn start does not match the coordinator request", ErrInvalidState)
		}
		id, present := e.Turn.Start.ProcessID()
		_, reused := ids[id]
		if !present && e.Phase != phaseFailed || reused {
			return fmt.Errorf("%w: turn process is absent or reused by a task", ErrInvalidState)
		}
	}
	if e.Mode == Undecided {
		if e.Turn.Outcome != nil && e.Phase != phaseFailed || len(e.Tasks) != len(e.Turn.Input.Tasks) ||
			!sameJSON(e.State, e.Turn.Input.State) || !sameJSON(e.Controls, nilIfEmpty(e.Turn.Input.Controls)) {
			return fmt.Errorf("%w: turn without a decision changed state, tasks, or controls", ErrInvalidState)
		}
		return nil
	}
	return e.validateAppliedDecision(ctx, d)
}

func (e executionState) validateAppliedDecision(ctx context.Context, d *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		e.Mode != decision.Mode || !sameJSON(e.State, decision.State) || !bytes.Equal(e.Output.JSON(), decision.Output.JSON()) {
		return fmt.Errorf("%w: applied state does not match the coordinator decision", ErrInvalidState)
	}
	for index, request := range decision.Tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !sameJSON(request, e.Tasks[previousCount+index].Request) {
			return fmt.Errorf("%w: applied task %d does not match the coordinator decision", ErrInvalidState, index)
		}
	}
	for index, control := range decision.Controls {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !sameJSON(control, e.Controls[index].Control) {
			return fmt.Errorf("%w: applied control %d does not match the coordinator decision", ErrInvalidState, index)
		}
	}
	before := e
	before.Tasks = e.Tasks[:previousCount]
	if err := before.validateDecision(ctx, d, decision); err != nil {
		return fmt.Errorf("%w: applied decision: %w", ErrInvalidState, err)
	}
	return ctx.Err()
}

func (e executionState) validateDecision(ctx context.Context, definition *Definition, decision Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := definition.descriptor.ValidateInput(decision.State); err != nil {
		return fmt.Errorf("%w: state: %w", ErrInvalidDecision, err)
	}
	if decision.Mode == Complete {
		if !decision.Output.Valid() || len(decision.Tasks) != 0 || len(decision.Controls) != 0 {
			return ErrInvalidDecision
		}
		if err := definition.descriptor.ValidateOutput(decision.Output); err != nil {
			return fmt.Errorf("%w: output: %w", ErrInvalidDecision, err)
		}
		return nil
	}
	if decision.Mode != Continue && decision.Mode != Wait || decision.Output.Valid() {
		return ErrInvalidDecision
	}
	if !definition.maxTasks.Allows(uint64(len(e.Tasks)), uint64(len(decision.Tasks))) ||
		uint64(len(e.remaining()))+uint64(len(decision.Tasks)) > uint64(definition.maxConcurrentTasks) ||
		uint64(len(decision.Controls)) > uint64(definition.maxControlsPerTurn) {
		return fmt.Errorf("%w: task or control bound exceeded", ErrInvalidDecision)
	}
	for index, request := range decision.Tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := definition.validateRequest(request); err != nil {
			return err
		}
		if e.task(request.Key) != nil {
			return fmt.Errorf("%w: reused task key", ErrInvalidDecision)
		}
		for _, previous := range decision.Tasks[:index] {
			if err := ctx.Err(); err != nil {
				return err
			}
			if previous.Key == request.Key {
				return fmt.Errorf("%w: duplicate task key", ErrInvalidDecision)
			}
		}
	}
	for _, control := range decision.Controls {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := e.controlEffect(control); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidDecision, err)
		}
	}
	if decision.Mode == Wait && len(e.remaining())+len(decision.Tasks) == 0 && !e.hasUnseenOutcome() {
		return fmt.Errorf("%w: wait has no outstanding tasks", ErrInvalidDecision)
	}
	return ctx.Err()
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
		return agent.NewChildSignalEffect(id, *control.Signal)
	}
	return agent.NewChildCancelEffect(id, *control.CancelReason)
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

func turnKey(number uint64) (agent.ChildKey, error) {
	return agent.ParseChildKey(fmt.Sprintf("%s%d", turnPrefix, number))
}
