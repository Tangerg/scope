package collaboration

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
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
)

// turnExecution records what only the turn owns: its number, the working
// state it opened with, and whether a task outcome arrived after it opened. The
// coordinator's Turn input is assembled from these, the current tasks and
// controls, and the configured workers when the turn starts.
type turnExecution struct {
	Number uint64        `json:"number"`
	State  agent.Payload `json:"state"`
	// OutcomeArrived reports a task outcome that arrived while this turn ran
	// and so is new to the next coordinator turn. The tasks own which ones.
	OutcomeArrived bool                    `json:"outcome_arrived,omitempty"`
	Start          *agent.ChildStartResult `json:"start,omitzero"`
	Outcome        *agent.ChildOutcome     `json:"outcome,omitzero"`
}

// processID names the coordinator child while it runs or after it finished.
func (t turnExecution) processID() (agent.ProcessID, bool) {
	if t.Outcome != nil {
		return t.Outcome.Result().ProcessID(), true
	}
	if t.Start != nil {
		return t.Start.ProcessID()
	}
	return agent.ProcessID{}, false
}

// decision decodes the recorded turn outcome. A failed turn is never
// recorded: it ends the collaboration when it arrives.
func (t turnExecution) decision() (Decision, error) {
	if t.Outcome == nil {
		return Decision{}, nil
	}
	result := t.Outcome.Result()
	output, present := result.Termination().Output()
	if !present || result.Termination().Status() != agent.StatusCompleted {
		return Decision{}, fmt.Errorf("%w: coordinator ended with %s: %s", ErrInvalidDecision, result.Termination().Status(), result.Termination().Reason())
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

// collaborationWaitKey names the one wait a collaboration holds open at a
// time. A Step consumes the previous wait's answer before it opens the next,
// so the key never names two open waits.
const collaborationWaitKey = "collaboration.wait"

type executionState struct {
	InitialState agent.Payload    `json:"initial_state,omitzero"`
	Tasks        []Task           `json:"tasks,omitempty"`
	Controls     []ControlReceipt `json:"controls,omitempty"`
	Turn         *turnExecution   `json:"turn,omitzero"`
	WaitID       *agent.WaitID    `json:"wait_id,omitzero"`
	// Completed marks a finished collaboration. The Engine owns the Output
	// its final Decision produced, so the state keeps nothing else.
	Completed bool `json:"completed,omitzero"`
}

// executionStateRecord carries the state's own fields without its encoding
// methods.
type executionStateRecord executionState

// executionStateDocument is the persisted state. While the current turn holds
// the Decision that declared them, the newest tasks and every control receipt
// keep only their kernel facts: the Decision owns their requests. The next
// turn replaces that Decision, so the records it leaves behind keep their own.
type executionStateDocument struct {
	executionStateRecord
	Tasks    []taskDocument           `json:"tasks,omitempty"`
	Controls []controlReceiptDocument `json:"controls,omitempty"`
}

type taskDocument struct {
	Task
	Request *TaskRequest `json:"request,omitzero"`
}

type controlReceiptDocument struct {
	ControlReceipt
	Control *Control `json:"control,omitzero"`
}

func (e executionState) MarshalJSON() ([]byte, error) {
	decision, err := e.decision()
	if err != nil {
		return nil, err
	}
	declared := len(e.Tasks) - len(decision.Tasks)
	if declared < 0 || len(decision.Controls) != 0 && len(e.Controls) != len(decision.Controls) {
		return nil, fmt.Errorf("%w: applied actions do not match the coordinator decision", ErrInvalidExecutionState)
	}
	document := executionStateDocument{executionStateRecord: executionStateRecord(e)}
	for index, task := range e.Tasks {
		entry := taskDocument{Task: task}
		if index < declared {
			entry.Request = new(task.Request)
		}
		document.Tasks = append(document.Tasks, entry)
	}
	for _, receipt := range e.Controls {
		entry := controlReceiptDocument{ControlReceipt: receipt}
		if len(decision.Controls) == 0 {
			entry.Control = new(receipt.Control)
		}
		document.Controls = append(document.Controls, entry)
	}
	return jsonv2.Marshal(document)
}

func (e *executionState) UnmarshalJSON(data []byte) error {
	document, err := jsonwire.Decode[executionStateDocument](data)
	if err != nil {
		return err
	}
	state := executionState(document.executionStateRecord)
	decision, err := state.decision()
	if err != nil {
		return err
	}
	declared := len(document.Tasks) - len(decision.Tasks)
	if declared < 0 || len(decision.Controls) != 0 && len(document.Controls) != len(decision.Controls) {
		return fmt.Errorf("%w: applied actions do not match the coordinator decision", ErrInvalidExecutionState)
	}
	state.Tasks = nil
	for index, entry := range document.Tasks {
		task := entry.Task
		switch {
		case index < declared && entry.Request != nil:
			task.Request = *entry.Request
		case index >= declared && entry.Request == nil:
			task.Request = decision.Tasks[index-declared]
		default:
			return fmt.Errorf("%w: task %d request belongs to the decision that declared it", ErrInvalidExecutionState, index)
		}
		state.Tasks = append(state.Tasks, task)
	}
	state.Controls = nil
	for index, entry := range document.Controls {
		receipt := entry.ControlReceipt
		switch {
		case len(decision.Controls) == 0 && entry.Control != nil:
			receipt.Control = *entry.Control
		case len(decision.Controls) != 0 && entry.Control == nil:
			receipt.Control = decision.Controls[index]
		default:
			return fmt.Errorf("%w: control %d belongs to the decision that declared it", ErrInvalidExecutionState, index)
		}
		state.Controls = append(state.Controls, receipt)
	}
	*e = state
	return nil
}

// phase derives the next protocol step.
func (e executionState) phase(decision Decision) phase {
	switch {
	case e.Completed:
		return phaseCompleted
	case e.Turn == nil:
		return phaseReady
	case e.Turn.Start == nil && e.Turn.Outcome == nil:
		return phaseStartingTurn
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
	if !decision.decided() {
		return e.Turn.State
	}
	return decision.State
}

// unapplied counts decided actions whose Framework settlement is still owed.
func (e executionState) unapplied() int {
	count := 0
	for _, task := range e.Tasks {
		if task.Start == nil && task.Outcome == nil {
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
		if task.Start == nil {
			continue
		}
		if id, present := task.Start.ProcessID(); present {
			ids = append(ids, id)
		}
	}
	if e.Turn != nil && e.Turn.Start != nil {
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
		child := &batch.Children[index]
		child.Key = task.Request.Key
		if task.Start != nil || task.Outcome != nil {
			child.ProcessID, _ = task.processID()
			child.Done = task.Start == nil || !child.ProcessID.Valid()
		}
	}
	if e.Turn != nil {
		key, err := turnKey(e.number())
		if err != nil {
			return childcall.Batch{}, err
		}
		child := childcall.Child{Key: key}
		if e.Turn.Start != nil || e.Turn.Outcome != nil {
			child.ProcessID, _ = e.Turn.processID()
			child.Done = e.Turn.Start == nil || !child.ProcessID.Valid()
		}
		batch.Children = append(batch.Children, child)
	}
	return batch, nil
}

func (e executionState) waitSpec(d *Definition) (agent.ChildWaitSpec, error) {
	key, err := agent.ParseWaitKey(collaborationWaitKey)
	if err != nil {
		return agent.ChildWaitSpec{}, err
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

// recordOutcome replaces the child's start receipt, whose ProcessID the
// outcome's Result already names.
func (e *executionState) recordOutcome(index int, outcome agent.ChildOutcome) {
	if index == len(e.Tasks) {
		e.Turn.Start, e.Turn.Outcome = nil, &outcome
		return
	}
	e.Tasks[index].Start, e.Tasks[index].Outcome = nil, &outcome
	e.Turn.OutcomeArrived = true
}

// validateOutcomes requires each drained child to keep only its outcome,
// which names the child and the subtree facts its drained wait established.
func (e executionState) validateOutcomes(ctx context.Context) error {
	for index, task := range e.Tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateDrainedOutcome(task.Start, task.Outcome); err != nil {
			return fmt.Errorf("%w: task %d: %w", ErrInvalidExecutionState, index, err)
		}
	}
	if e.Turn != nil {
		if err := validateDrainedOutcome(e.Turn.Start, e.Turn.Outcome); err != nil {
			return fmt.Errorf("%w: turn: %w", ErrInvalidExecutionState, err)
		}
	}
	return ctx.Err()
}

func validateDrainedOutcome(start *agent.ChildStartResult, outcome *agent.ChildOutcome) error {
	if outcome == nil {
		return nil
	}
	if start != nil {
		return errors.New("finished child retains its start receipt")
	}
	if _, drained := outcome.SubtreeUnresolvedEffects(); !drained {
		return errors.New("child outcome lacks its drained subtree")
	}
	return nil
}

func (e executionState) validate(ctx context.Context, d *Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.Completed {
		if e.InitialState.Valid() || len(e.Tasks) != 0 || len(e.Controls) != 0 || e.Turn != nil || e.WaitID != nil {
			return fmt.Errorf("%w: completed collaboration retains state beyond its completion marker", ErrInvalidExecutionState)
		}
		return nil
	}
	decision, decisionErr := e.decision()
	if decisionErr != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, decisionErr)
	}
	if decision.completes() {
		return fmt.Errorf("%w: a completing decision ends the collaboration", ErrInvalidExecutionState)
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
	if err := e.validateOutcomes(ctx); err != nil {
		return err
	}
	pending, err := e.validateTasks(ctx, d)
	if err != nil {
		return err
	}
	pendingControls, err := e.validateControls(ctx, pending)
	if err != nil {
		return err
	}
	current := e.phase(decision)
	if current == phaseReady {
		return e.validateReady()
	}
	if err := e.validateTurn(ctx, d, current, decision); err != nil {
		return err
	}
	if err := e.validatePhaseEvidence(current, pending+pendingControls); err != nil {
		return err
	}
	if err := e.validatePhaseProgress(d, current, decision.Mode); err != nil {
		return err
	}
	// The child batch owns key and Process uniqueness across tasks and turn.
	batch, batchErr := e.batch(d)
	if batchErr == nil {
		batchErr = batch.Validate()
	}
	if batchErr != nil {
		return fmt.Errorf("%w: children: %w", ErrInvalidExecutionState, batchErr)
	}
	return ctx.Err()
}

func (e executionState) validateBounds(d *Definition) error {
	if !d.maxTurns.Allows(e.number()) || !d.maxTasks.Allows(uint64(len(e.Tasks))) || uint64(len(e.Controls)) > uint64(d.maxControlsPerTurn) {
		return fmt.Errorf("%w: turn, task, or control bound exceeded", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateReady() error {
	if len(e.Tasks)+len(e.Controls) != 0 || e.WaitID != nil {
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
		return fmt.Errorf("%w: starting turn retains an outcome", ErrInvalidExecutionState)
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
	if e.Turn.Outcome != nil && !e.awaitsTasks(mode) {
		return fmt.Errorf("%w: a decided turn waits only for running tasks with no unseen outcome", ErrInvalidExecutionState)
	}
	if _, err := e.waitSpec(d); err != nil {
		return fmt.Errorf("%w: child wait: %w", ErrInvalidExecutionState, err)
	}
	return nil
}

func (e executionState) validateTasks(ctx context.Context, d *Definition) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	pending, active := 0, 0
	for index, task := range e.Tasks {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := d.validateRequest(task.Request); err != nil {
			return 0, fmt.Errorf("%w: task %d request: %w", ErrInvalidExecutionState, index, err)
		}
		worker, _ := d.worker(task.Request.Worker)
		if task.Start == nil && task.Outcome == nil {
			pending++
		} else {
			if pending > 0 {
				return 0, fmt.Errorf("%w: task %d start follows a pending start", ErrInvalidExecutionState, index)
			}
			if _, present := task.processID(); present && task.Outcome == nil {
				active++
			}
		}
		if task.Outcome != nil {
			if output, completed := task.Outcome.Result().Termination().Output(); completed {
				if err := worker.deployment.Descriptor().ValidateOutput(output); err != nil {
					return 0, fmt.Errorf("%w: task %d output: %w", ErrInvalidExecutionState, index, err)
				}
			}
		}
	}
	if uint64(active+pending) > uint64(d.maxConcurrentTasks) {
		return 0, fmt.Errorf("%w: active and pending tasks exceed concurrency bound", ErrInvalidExecutionState)
	}
	return pending, nil
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
		if _, err := receipt.Control.effect(tasks[receipt.Control.Task]); err != nil {
			return 0, fmt.Errorf("%w: control %d: %w", ErrInvalidExecutionState, index, err)
		}
		if receipt.Result == nil {
			pendingControls++
		} else if pending > 0 || pendingControls > 0 {
			return 0, fmt.Errorf("%w: control %d result precedes pending work", ErrInvalidExecutionState, index)
		}
	}
	return pendingControls, nil
}

func (e executionState) validateTurn(ctx context.Context, d *Definition, current phase, decision Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.validateTurnStart(); err != nil {
		return err
	}
	if !decision.decided() {
		return e.validateUndecidedTurn()
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
	if e.Turn.OutcomeArrived && !slices.ContainsFunc(e.Tasks, func(task Task) bool { return task.Outcome != nil }) {
		return fmt.Errorf("%w: an arrived task outcome is not recorded", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateTurnStart() error {
	if e.Turn.Start == nil && e.Turn.Outcome == nil {
		return nil
	}
	if _, present := e.Turn.processID(); !present {
		return fmt.Errorf("%w: turn process is absent", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateUndecidedTurn() error {
	if e.Turn.Outcome != nil {
		return fmt.Errorf("%w: turn without a decision retains an outcome", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateAppliedDecision(ctx context.Context, d *Definition, decision Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	output, _ := e.Turn.Outcome.Result().Termination().Output()
	if err := d.coordinator.deployment.Descriptor().ValidateOutput(output); err != nil {
		return fmt.Errorf("%w: coordinator output: %w", ErrInvalidExecutionState, err)
	}
	before := e
	before.Tasks = e.Tasks[:len(e.Tasks)-len(decision.Tasks)]
	if err := before.validateDecision(ctx, d, decision); err != nil {
		return fmt.Errorf("%w: applied decision: %w", ErrInvalidExecutionState, err)
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
	if err := decision.validateShape(); err != nil {
		return err
	}
	if decision.completes() {
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

// awaitsTasks reports whether a decided turn waits for its running tasks
// rather than starting the next turn. Step branches on it and Restore
// requires it of a waiting decided turn.
func (e executionState) awaitsTasks(mode Mode) bool {
	return mode == ModeWait && !e.hasUnseenOutcome() && len(e.remaining()) != 0
}

func (e executionState) hasUnseenOutcome() bool {
	return e.Turn != nil && e.Turn.OutcomeArrived
}

const turnPrefix = "collaboration.turn."

func turnKey(number uint64) (agent.ChildKey, error) {
	return agent.ParseChildKey(fmt.Sprintf("%s%d", turnPrefix, number))
}
