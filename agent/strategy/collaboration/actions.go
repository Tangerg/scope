package collaboration

import (
	"fmt"
	"strings"

	agent "github.com/Tangerg/scope/agent"
)

const turnPrefix = "collaboration.turn."

func turnKey(number uint32) (agent.ChildKey, error) {
	return agent.ParseChildKey(fmt.Sprintf("%s%d", turnPrefix, number))
}

func (d *Definition) validateRequest(request TaskRequest) error {
	if !request.Key.Valid() || strings.HasPrefix(request.Key.String(), turnPrefix) {
		return ErrInvalidDecision
	}
	worker, found := d.worker(request.Worker)
	if !found {
		return fmt.Errorf("%w: unknown worker %q", ErrInvalidDecision, request.Worker)
	}
	if err := worker.descriptor.ValidateInput(request.Input); err != nil {
		return fmt.Errorf("%w: task input: %w", ErrInvalidDecision, err)
	}
	return nil
}

func (e *execution) applyDecision(decision Decision, consumed uint32) (agent.Transition, error) {
	if err := e.validateDecision(decision); err != nil {
		return agent.Transition{}, err
	}
	effects := make([]agent.Effect, 0, len(decision.Tasks)+len(decision.Controls))
	for _, request := range decision.Tasks {
		worker, _ := e.definition.worker(request.Worker)
		effect, err := agent.StartChild(worker.spec(request.Key, request.Input))
		if err != nil {
			return agent.Transition{}, err
		}
		effects = append(effects, effect)
	}
	for _, control := range decision.Controls {
		effect, err := e.state.controlEffect(control)
		if err != nil {
			return agent.Transition{}, err
		}
		effects = append(effects, effect)
	}
	e.state.State = decision.State
	e.state.Mode = decision.Mode
	e.state.Controls = nil
	for _, request := range decision.Tasks {
		e.state.Tasks = append(e.state.Tasks, Task{Request: request})
	}
	for _, control := range decision.Controls {
		e.state.Controls = append(e.state.Controls, ControlReceipt{Control: control})
	}
	if decision.Mode == Complete {
		e.state.Phase = phaseCompleted
		e.state.Output = decision.Output
		return agent.Complete(consumed, *decision.Output)
	}
	if len(effects) == 0 {
		return e.afterActions(consumed)
	}
	e.state.Phase = phaseApplying
	return agent.Continue(consumed, effects...)
}

func (e *execution) validateDecision(decision Decision) error {
	definition := e.definition
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
	if uint64(len(e.state.Tasks))+uint64(len(decision.Tasks)) > uint64(definition.maxTasks) ||
		uint64(len(e.state.remaining()))+uint64(len(decision.Tasks)) > uint64(definition.maxConcurrentTasks) ||
		uint64(len(decision.Controls)) > uint64(definition.maxControlsPerTurn) {
		return fmt.Errorf("%w: task or control bound exceeded", ErrInvalidDecision)
	}
	for index, request := range decision.Tasks {
		if err := e.definition.validateRequest(request); err != nil {
			return err
		}
		if e.state.task(request.Key) != nil {
			return fmt.Errorf("%w: reused task key", ErrInvalidDecision)
		}
		for _, previous := range decision.Tasks[:index] {
			if previous.Key == request.Key {
				return fmt.Errorf("%w: duplicate task key", ErrInvalidDecision)
			}
		}
	}
	for _, control := range decision.Controls {
		if _, err := e.state.controlEffect(control); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidDecision, err)
		}
	}
	if decision.Mode == Wait && len(e.state.remaining())+len(decision.Tasks) == 0 && !e.state.turnHadOutstandingTask() {
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

func (e executionState) turnHadOutstandingTask() bool {
	if e.Turn == nil {
		return false
	}
	for _, task := range e.Turn.Input.Tasks {
		if task.Start == nil || task.Outcome != nil {
			continue
		}
		if _, started := task.Start.ProcessID(); started {
			return true
		}
	}
	return false
}
