package collaboration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
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
		return agent.ChildWaitSpec{}, ErrInvalidState
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
	return present && outcome.Key() == start.Key() && outcome.Result().ProcessID() == id
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
	if err := d.config.StateSchema.ValidateInput(e.State); err != nil {
		return fmt.Errorf("%w: state: %w", ErrInvalidState, err)
	}
	if e.Number > d.config.MaxTurns || uint64(len(e.Tasks)) > uint64(d.config.MaxTasks) ||
		uint64(len(e.Controls)) > uint64(d.config.MaxControlsPerTurn) || e.WaitSequence > uint64(e.Number)+uint64(len(e.Tasks)) {
		return ErrInvalidState
	}
	pending, active := 0, 0
	var ids []agent.ProcessID
	for index, task := range e.Tasks {
		if err := d.validateRequest(task.Request); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidState, err)
		}
		for _, previous := range e.Tasks[:index] {
			if previous.Request.Key == task.Request.Key {
				return ErrInvalidState
			}
		}
		worker, _ := d.worker(task.Request.Worker)
		if task.Start == nil {
			pending++
		} else {
			if pending > 0 || !task.Start.Valid() || task.Start.Key() != task.Request.Key || task.Start.DeploymentRef() != worker.Deployment.DeploymentRef() {
				return ErrInvalidState
			}
			if id, present := task.Start.ProcessID(); present {
				if slices.Contains(ids, id) {
					return ErrInvalidState
				}
				ids = append(ids, id)
				if task.Outcome == nil {
					active++
				}
			}
		}
		if !matchesOutcome(task.Start, task.Outcome) {
			return ErrInvalidState
		}
		if task.Outcome != nil {
			if output, completed := task.Outcome.Result().Output(); completed {
				if err := worker.Deployment.Descriptor().ValidateOutput(output); err != nil {
					return fmt.Errorf("%w: task output: %w", ErrInvalidState, err)
				}
			}
		}
	}
	if uint64(active+pending) > uint64(d.config.MaxConcurrentTasks) {
		return ErrInvalidState
	}
	pendingControls := 0
	for _, receipt := range e.Controls {
		effect, err := e.controlEffect(receipt.Control)
		if err != nil {
			return fmt.Errorf("%w: control: %w", ErrInvalidState, err)
		}
		if receipt.Result == nil {
			pendingControls++
		} else if pending > 0 || pendingControls > 0 || !receipt.Result.Matches(effect) {
			return ErrInvalidState
		}
	}
	if e.Phase == phaseReady {
		if e.Number != 0 || len(e.Tasks)+len(e.Controls) != 0 || e.Turn != nil || e.Mode != "" || e.WaitSequence != 0 || e.WaitID != nil || e.Output != nil {
			return ErrInvalidState
		}
		return nil
	}
	if err := e.validateTurn(d, ids); err != nil {
		return err
	}
	if e.Phase != phaseApplying && pending+pendingControls != 0 {
		return ErrInvalidState
	}
	if (e.Phase == phaseWaiting) != (e.WaitID != nil) || e.WaitID != nil && !e.WaitID.Valid() {
		return ErrInvalidState
	}
	if (e.Phase == phaseCompleted) != (e.Output != nil) {
		return ErrInvalidState
	}
	switch e.Phase {
	case phaseStartingTurn:
		if e.Turn.Start == nil && e.Turn.Outcome == nil && e.Mode == "" {
			return nil
		}
	case phaseApplying:
		if e.Turn.Outcome != nil && pending+pendingControls > 0 && (e.Mode == Continue || e.Mode == Wait) {
			return nil
		}
	case phaseOpening, phaseWaiting:
		if e.Turn.Start == nil {
			break
		}
		if e.Turn.Outcome == nil && e.Mode != "" || e.Turn.Outcome != nil && e.Mode != Wait {
			break
		}
		if _, err := e.waitSpec(); err == nil {
			return nil
		}
	case phaseCompleted:
		if e.Turn.Outcome != nil && e.Mode == Complete && d.config.OutputSchema.ValidateOutput(*e.Output) == nil {
			return nil
		}
	case phaseFailed:
		if _, failed := e.Turn.failure(); failed && e.Mode == "" {
			return nil
		}
	}
	return ErrInvalidState
}

func (e executionState) validateTurn(d *Definition, ids []agent.ProcessID) error {
	if e.Turn == nil || e.Number == 0 || e.Turn.Input.Number != e.Number || len(e.Turn.Input.Tasks) > len(e.Tasks) ||
		d.config.StateSchema.ValidateInput(e.Turn.Input.State) != nil {
		return ErrInvalidState
	}
	var descriptors []agent.Descriptor
	for _, worker := range d.config.Workers {
		descriptors = append(descriptors, worker.Deployment.Descriptor())
	}
	if !sameJSON(descriptors, e.Turn.Input.Workers) {
		return ErrInvalidState
	}
	for index, task := range e.Turn.Input.Tasks {
		current := e.Tasks[index]
		if !sameJSON(task.Request, current.Request) || task.Start == nil || !sameJSON(task.Start, current.Start) ||
			task.Outcome != nil && !sameJSON(task.Outcome, current.Outcome) {
			return ErrInvalidState
		}
	}
	if uint64(len(e.Turn.Input.Controls)) > uint64(d.config.MaxControlsPerTurn) {
		return ErrInvalidState
	}
	captured := executionState{Tasks: e.Turn.Input.Tasks}
	for _, receipt := range e.Turn.Input.Controls {
		effect, err := captured.controlEffect(receipt.Control)
		if err != nil || receipt.Result == nil || !receipt.Result.Matches(effect) {
			return ErrInvalidState
		}
	}
	if e.Turn.Start != nil {
		key, err := turnKey(e.Number)
		if err != nil || !e.Turn.Start.Valid() || e.Turn.Start.Key() != key || e.Turn.Start.DeploymentRef() != d.config.Coordinator.Deployment.DeploymentRef() {
			return ErrInvalidState
		}
		id, present := e.Turn.Start.ProcessID()
		if !present && e.Phase != phaseFailed || slices.Contains(ids, id) {
			return ErrInvalidState
		}
	}
	if !matchesOutcome(e.Turn.Start, e.Turn.Outcome) {
		return ErrInvalidState
	}
	if e.Mode == "" {
		if e.Turn.Outcome != nil && e.Phase != phaseFailed || len(e.Tasks) != len(e.Turn.Input.Tasks) ||
			!sameJSON(e.State, e.Turn.Input.State) || !sameJSON(e.Controls, nilIfEmpty(e.Turn.Input.Controls)) {
			return ErrInvalidState
		}
		return nil
	}
	return e.validateAppliedDecision(d)
}

func (e executionState) validateAppliedDecision(d *Definition) error {
	if e.Turn.Outcome == nil {
		return ErrInvalidState
	}
	result := e.Turn.Outcome.Result()
	output, present := result.Output()
	if !present || result.Status() != agent.StatusCompleted {
		return ErrInvalidState
	}
	decision, err := d.config.Coordinator.Deployment.Descriptor().DecodeOutput[Decision](output)
	if err != nil {
		return fmt.Errorf("%w: coordinator output: %w", ErrInvalidState, err)
	}
	previousCount := len(e.Turn.Input.Tasks)
	if len(e.Tasks) != previousCount+len(decision.Tasks) || len(e.Controls) != len(decision.Controls) ||
		e.Mode != decision.Mode || !sameJSON(e.State, decision.State) || !sameJSON(e.Output, decision.Output) {
		return ErrInvalidState
	}
	for index, request := range decision.Tasks {
		if !sameJSON(request, e.Tasks[previousCount+index].Request) {
			return ErrInvalidState
		}
	}
	for index, control := range decision.Controls {
		if !sameJSON(control, e.Controls[index].Control) {
			return ErrInvalidState
		}
	}
	before := e
	before.Tasks = e.Tasks[:previousCount]
	candidate := execution{definition: d, state: before}
	if err := candidate.validateDecision(decision); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidState, err)
	}
	return nil
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
