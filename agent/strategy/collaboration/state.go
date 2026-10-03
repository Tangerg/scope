package collaboration

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

// phase names the next protocol step. It is derived from the recorded turn,
// decision, and child evidence, never persisted, so no stored phase can
// contradict that evidence.
type phase uint8

const (
	phaseReady phase = iota
	phaseStartingTurn
	phaseApplying
	phaseOpening
	phaseWaiting
	phaseCompleted
	phaseFailed
)

// turnExecution records what only the turn owns: its number, the working
// state it opened with, and which task outcomes arrived after it opened. The
// coordinator's Turn input is assembled from these, the current tasks and
// controls, and the configured workers when the turn starts.
type turnExecution struct {
	Number uint64        `json:"number"`
	State  agent.Payload `json:"state"`
	// UnseenOutcomes lists, in arrival order, the tasks whose outcomes arrived
	// while this turn ran and so are new to the next coordinator turn.
	UnseenOutcomes []uint32                `json:"unseen_outcomes,omitempty"`
	Start          *agent.ChildStartResult `json:"start,omitzero"`
	Outcome        *agent.ChildOutcome     `json:"outcome,omitzero"`
}

func (t turnExecution) unresolved() bool {
	return t.Outcome != nil && !t.Outcome.SubtreeResolved()
}

// ended reports a turn that failed to produce a usable decision.
func (t turnExecution) ended() bool {
	_, failed := t.failure()
	return failed || t.unresolved()
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

func (t turnExecution) decision() (Decision, error) {
	if t.Outcome == nil || t.ended() {
		return Decision{}, nil
	}
	result := t.Outcome.Result()
	output, present := result.Output()
	if !present || result.Status() != agent.StatusCompleted {
		return Decision{}, fmt.Errorf("%w: coordinator ended with %s: %s", ErrInvalidDecision, result.Status(), result.Termination().Reason())
	}
	decision, err := output.Decode[Decision]()
	if err != nil {
		return Decision{}, fmt.Errorf("%w: coordinator output: %w", ErrInvalidDecision, err)
	}
	if err := decision.validateShape(); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

type executionState struct {
	InitialState agent.Payload    `json:"initial_state,omitzero"`
	Tasks        []Task           `json:"tasks,omitempty"`
	Controls     []ControlReceipt `json:"controls,omitempty"`
	Turn         *turnExecution   `json:"turn,omitzero"`
	WaitSequence uint64           `json:"wait_sequence"`
	WaitID       *agent.WaitID    `json:"wait_id,omitzero"`
}

func (e *executionState) UnmarshalJSON(data []byte) error {
	type wire executionState
	decoded, err := jsonwire.Decode[wire](data, "wait_sequence")
	if err != nil {
		return err
	}
	*e = executionState(decoded)
	return nil
}

// phase derives the next protocol step. A turn fails, before any decision
// applies, when its start fails or its drained outcome failed or retains
// unresolved Effects.
func (e executionState) phase(mode Mode) phase {
	switch {
	case e.Turn == nil:
		return phaseReady
	case mode == ModeComplete:
		return phaseCompleted
	case e.Turn.Start == nil:
		return phaseStartingTurn
	case mode == ModeUndecided && e.Turn.ended():
		return phaseFailed
	case e.unapplied() != 0:
		return phaseApplying
	case e.WaitID != nil:
		return phaseWaiting
	default:
		return phaseOpening
	}
}

func (e executionState) number() uint64 {
	if e.Turn == nil {
		return 0
	}
	return e.Turn.Number
}

func (e executionState) decision() (Decision, error) {
	if e.Turn == nil {
		return Decision{}, nil
	}
	return e.Turn.decision()
}

func (e executionState) workingState(decision Decision) agent.Payload {
	if e.Turn == nil {
		return e.InitialState
	}
	if decision.Mode == ModeUndecided {
		return e.Turn.State
	}
	return decision.State
}

// unapplied counts decided actions whose Framework settlement is still owed.
func (e executionState) unapplied() int {
	count := 0
	for _, task := range e.Tasks {
		if task.Start == nil {
			count++
		}
	}
	for _, receipt := range e.Controls {
		if receipt.Result == nil {
			count++
		}
	}
	return count
}

func (e executionState) taskIndex() map[agent.ChildKey]*Task {
	tasks := make(map[agent.ChildKey]*Task, len(e.Tasks))
	for index := range e.Tasks {
		tasks[e.Tasks[index].Request.Key] = &e.Tasks[index]
	}
	return tasks
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
		child.Key, child.Deployment = task.Request.Key, worker.deployment.DeploymentRef()
		if task.Start != nil {
			id, started := task.Start.ProcessID()
			child.ProcessID, child.Done = id, !started || task.Outcome != nil
		}
	}
	if e.Turn != nil {
		key, err := turnKey(e.number())
		if err != nil {
			return childcall.Batch{}, err
		}
		child := childcall.Child{Key: key, Deployment: d.coordinator.deployment.DeploymentRef()}
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
		return agent.ChildWaitSpec{}, fmt.Errorf("%w: wait requires a sequence", ErrInvalidExecutionState)
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
	e.Turn.UnseenOutcomes = append(e.Turn.UnseenOutcomes, uint32(index))
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
	for _, outcome := range outcomes {
		if outcome.Boundary() != agent.ChildWaitBoundaryDrained {
			return fmt.Errorf("%w: child outcome requires drained boundary", ErrInvalidExecutionState)
		}
	}
	indices, err := batch.MatchOutcomes(outcomes)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if !slices.Equal(indices, expected) {
		return fmt.Errorf("%w: outcome belongs to another child", ErrInvalidExecutionState)
	}
	return ctx.Err()
}

func (e executionState) validate(ctx context.Context, d *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	decision, decisionErr := e.decision()
	if decisionErr != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, decisionErr)
	}
	if e.Turn != nil && e.InitialState.Valid() {
		return fmt.Errorf("%w: initial state retained after the first turn", ErrInvalidExecutionState)
	}
	if e.Turn != nil {
		if err := e.validateTurnInput(d); err != nil {
			return err
		}
	}
	if err := d.descriptor.ValidateInput(e.workingState(decision)); err != nil {
		return fmt.Errorf("%w: state: %w", ErrInvalidExecutionState, err)
	}
	if err := e.validateBounds(d); err != nil {
		return err
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
	current := e.phase(decision.Mode)
	if current == phaseReady {
		return e.validateReady()
	}
	if err := e.validateTurn(ctx, d, ids, current, decision); err != nil {
		return err
	}
	if err := e.validatePhaseEvidence(current, pending+pendingControls); err != nil {
		return err
	}
	if err := e.validatePhaseProgress(d, current, decision.Mode); err != nil {
		return err
	}
	return ctx.Err()
}

func (e executionState) validateBounds(d *Definition) error {
	if !d.maxTurns.Allows(e.number()) || !d.maxTasks.Allows(uint64(len(e.Tasks))) || uint64(len(e.Controls)) > uint64(d.maxControlsPerTurn) {
		return fmt.Errorf("%w: turn, task, or control bound exceeded", ErrInvalidExecutionState)
	}
	if e.WaitSequence > e.number() && e.WaitSequence-e.number() > uint64(len(e.Tasks)) {
		return fmt.Errorf("%w: wait sequence exceeds declared turns and tasks", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateReady() error {
	if len(e.Tasks)+len(e.Controls) != 0 || e.WaitSequence != 0 || e.WaitID != nil {
		return fmt.Errorf("%w: ready phase retains execution progress", ErrInvalidExecutionState)
	}
	return nil
}

// validatePhaseEvidence rejects evidence the derived phase leaves unexplained:
// owed settlements outside applying and a wait identity outside waiting.
func (e executionState) validatePhaseEvidence(current phase, unapplied int) error {
	if current != phaseApplying && unapplied != 0 {
		return fmt.Errorf("%w: settled turn retains unapplied work", ErrInvalidExecutionState)
	}
	if e.WaitID != nil && (current != phaseWaiting || !e.WaitID.Valid()) {
		return fmt.Errorf("%w: wait identity without an open wait", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validatePhaseProgress(d *Definition, current phase, mode Mode) error {
	switch current {
	case phaseStartingTurn:
		return e.validateStartingTurn()
	case phaseApplying:
		return e.validateApplying(mode)
	case phaseOpening, phaseWaiting:
		return e.validateWaitingTurn(d, mode)
	default:
		return nil
	}
}

func (e executionState) validateStartingTurn() error {
	if e.Turn.Outcome != nil {
		return fmt.Errorf("%w: starting turn retains a start, outcome, or decision", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateApplying(mode Mode) error {
	if mode != ModeContinue && mode != ModeWait {
		return fmt.Errorf("%w: applying phase requires a continuing decision and pending work", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateWaitingTurn(d *Definition, mode Mode) error {
	if mode == ModeWait && e.hasUnseenOutcome() {
		return fmt.Errorf("%w: waiting decision has unseen task outcomes", ErrInvalidExecutionState)
	}
	if e.Turn.Outcome != nil && mode != ModeWait {
		return fmt.Errorf("%w: waiting mode contradicts turn outcome", ErrInvalidExecutionState)
	}
	if _, err := e.waitSpec(d); err != nil {
		return fmt.Errorf("%w: child wait: %w", ErrInvalidExecutionState, err)
	}
	return nil
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
			return 0, nil, fmt.Errorf("%w: task %d request: %w", ErrInvalidExecutionState, index, err)
		}
		if _, duplicate := keys[task.Request.Key]; duplicate {
			return 0, nil, fmt.Errorf("%w: task %d duplicates key %q", ErrInvalidExecutionState, index, task.Request.Key)
		}
		keys[task.Request.Key] = struct{}{}
		worker, _ := d.worker(task.Request.Worker)
		if task.Start == nil {
			pending++
		} else {
			if pending > 0 {
				return 0, nil, fmt.Errorf("%w: task %d start follows a pending start", ErrInvalidExecutionState, index)
			}
			if !task.Start.Matches(task.Request.Key, worker.deployment.DeploymentRef()) {
				return 0, nil, fmt.Errorf("%w: task %d start does not match its request", ErrInvalidExecutionState, index)
			}
			if id, present := task.Start.ProcessID(); present {
				if _, duplicate := ids[id]; duplicate {
					return 0, nil, fmt.Errorf("%w: task %d reuses process %q", ErrInvalidExecutionState, index, id)
				}
				ids[id] = struct{}{}
				if task.Outcome == nil {
					active++
				}
			}
		}
		if task.Outcome != nil {
			if output, completed := task.Outcome.Result().Output(); completed {
				if err := worker.deployment.Descriptor().ValidateOutput(output); err != nil {
					return 0, nil, fmt.Errorf("%w: task %d output: %w", ErrInvalidExecutionState, index, err)
				}
			}
		}
	}
	if uint64(active+pending) > uint64(d.maxConcurrentTasks) {
		return 0, nil, fmt.Errorf("%w: active and pending tasks exceed concurrency bound", ErrInvalidExecutionState)
	}
	return pending, ids, nil
}

func (e executionState) validateControls(ctx context.Context, pending int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	tasks := e.taskIndex()
	pendingControls := 0
	for index, receipt := range e.Controls {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		effect, err := receipt.Control.effect(tasks[receipt.Control.Task])
		if err != nil {
			return 0, fmt.Errorf("%w: control %d: %w", ErrInvalidExecutionState, index, err)
		}
		if receipt.Result == nil {
			pendingControls++
		} else if pending > 0 || pendingControls > 0 {
			return 0, fmt.Errorf("%w: control %d result precedes pending work", ErrInvalidExecutionState, index)
		} else if !receipt.Result.Matches(effect) {
			return 0, fmt.Errorf("%w: control %d result does not match its effect", ErrInvalidExecutionState, index)
		}
	}
	return pendingControls, nil
}

func (e executionState) validateTurn(ctx context.Context, d *Definition, ids map[agent.ProcessID]struct{}, current phase, decision Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.validateTurnStart(d, ids, current); err != nil {
		return err
	}
	if decision.Mode == ModeUndecided {
		return e.validateUndecidedTurn(current)
	}
	return e.validateAppliedDecision(ctx, d, decision)
}

func (e executionState) validateTurnInput(d *Definition) error {
	if e.number() == 0 {
		return fmt.Errorf("%w: turn requires a positive number", ErrInvalidExecutionState)
	}
	if err := d.descriptor.ValidateInput(e.Turn.State); err != nil {
		return fmt.Errorf("%w: turn input state: %w", ErrInvalidExecutionState, err)
	}
	seen := make(map[uint32]struct{}, len(e.Turn.UnseenOutcomes))
	for _, index := range e.Turn.UnseenOutcomes {
		if _, repeated := seen[index]; repeated || uint64(index) >= uint64(len(e.Tasks)) || e.Tasks[index].Outcome == nil {
			return fmt.Errorf("%w: unseen task outcome %d is not a recorded outcome", ErrInvalidExecutionState, index)
		}
		seen[index] = struct{}{}
	}
	return nil
}

func (e executionState) validateTurnStart(d *Definition, ids map[agent.ProcessID]struct{}, current phase) error {
	if e.Turn.Start == nil {
		return nil
	}
	key, err := turnKey(e.number())
	if err != nil {
		return fmt.Errorf("%w: turn key: %w", ErrInvalidExecutionState, err)
	}
	if !e.Turn.Start.Matches(key, d.coordinator.deployment.DeploymentRef()) {
		return fmt.Errorf("%w: turn start does not match the coordinator request", ErrInvalidExecutionState)
	}
	id, present := e.Turn.Start.ProcessID()
	_, reused := ids[id]
	if !present && current != phaseFailed || reused {
		return fmt.Errorf("%w: turn process is absent or reused by a task", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateUndecidedTurn(current phase) error {
	if e.Turn.Outcome != nil && current != phaseFailed {
		return fmt.Errorf("%w: turn without a decision retains an outcome", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateAppliedDecision(ctx context.Context, d *Definition, decision Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	output, _ := e.Turn.Outcome.Result().Output()
	if err := d.coordinator.deployment.Descriptor().ValidateOutput(output); err != nil {
		return fmt.Errorf("%w: coordinator output: %w", ErrInvalidExecutionState, err)
	}
	if err := e.validateAppliedActions(ctx, decision); err != nil {
		return err
	}
	before := e
	before.Tasks = e.Tasks[:len(e.Tasks)-len(decision.Tasks)]
	if err := before.validateDecision(ctx, d, decision); err != nil {
		return fmt.Errorf("%w: applied decision: %w", ErrInvalidExecutionState, err)
	}
	return ctx.Err()
}

func (e executionState) validateAppliedActions(ctx context.Context, decision Decision) error {
	if len(e.Tasks) < len(decision.Tasks) || len(e.Controls) != len(decision.Controls) {
		return fmt.Errorf("%w: applied actions do not match the coordinator decision", ErrInvalidExecutionState)
	}
	previousCount := len(e.Tasks) - len(decision.Tasks)
	for index, request := range decision.Tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !sameJSON(request, e.Tasks[previousCount+index].Request) {
			return fmt.Errorf("%w: applied task %d does not match the coordinator decision", ErrInvalidExecutionState, index)
		}
	}
	for index, control := range decision.Controls {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !sameJSON(control, e.Controls[index].Control) {
			return fmt.Errorf("%w: applied control %d does not match the coordinator decision", ErrInvalidExecutionState, index)
		}
	}
	return nil
}

func (e executionState) validateDecision(ctx context.Context, definition *Definition, decision Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := definition.descriptor.ValidateInput(decision.State); err != nil {
		return fmt.Errorf("%w: state: %w", ErrInvalidDecision, err)
	}
	if err := decision.validateShape(); err != nil {
		return err
	}
	if decision.Mode == ModeComplete {
		if err := definition.descriptor.ValidateOutput(decision.Output); err != nil {
			return fmt.Errorf("%w: output: %w", ErrInvalidDecision, err)
		}
		return nil
	}
	remaining := len(e.remaining())
	if !definition.maxTasks.Allows(uint64(len(e.Tasks)), uint64(len(decision.Tasks))) ||
		uint64(remaining)+uint64(len(decision.Tasks)) > uint64(definition.maxConcurrentTasks) ||
		uint64(len(decision.Controls)) > uint64(definition.maxControlsPerTurn) {
		return fmt.Errorf("%w: task or control bound exceeded", ErrInvalidDecision)
	}
	if err := e.validateActions(ctx, definition, decision); err != nil {
		return err
	}
	if decision.Mode == ModeWait && remaining+len(decision.Tasks) == 0 && !e.hasUnseenOutcome() {
		return fmt.Errorf("%w: wait has no outstanding tasks", ErrInvalidDecision)
	}
	return ctx.Err()
}

func (e executionState) validateActions(ctx context.Context, definition *Definition, decision Decision) error {
	tasks := e.taskIndex()
	keys := make(map[agent.ChildKey]struct{}, len(decision.Tasks))
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
		if _, duplicate := keys[request.Key]; duplicate {
			return fmt.Errorf("%w: duplicate task key", ErrInvalidDecision)
		}
		keys[request.Key] = struct{}{}
	}
	for _, control := range decision.Controls {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := control.effect(tasks[control.Task]); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidDecision, err)
		}
	}
	return nil
}

func (e executionState) hasUnseenOutcome() bool {
	return e.Turn != nil && len(e.Turn.UnseenOutcomes) != 0
}

func sameJSON(left, right any) bool {
	first, err := jsonv2.Marshal(left)
	if err != nil {
		return false
	}
	second, err := jsonv2.Marshal(right)
	return err == nil && bytes.Equal(first, second)
}

const turnPrefix = "collaboration.turn."

func turnKey(number uint64) (agent.ChildKey, error) {
	return agent.ParseChildKey(fmt.Sprintf("%s%d", turnPrefix, number))
}
