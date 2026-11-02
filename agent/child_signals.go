package agent

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
)

func collectChildStarts(signals []run.Signal) ([]run.ChildStartResult, steerBatch, uint32, error) {
	starts := make([]run.ChildStartResult, 0, len(signals))
	var steer steerBatch
	for _, signal := range signals {
		if recognized, err := steer.collectSignal(signal); err != nil {
			return nil, steerBatch{}, 0, err
		} else if recognized {
			continue
		}
		start, err := signal.Frame[run.ChildStartResult]()
		if err != nil {
			return nil, steerBatch{}, 0, fmt.Errorf("%w: invalid child-start Signal: %w", ErrInvalidExecutionState, err)
		}
		starts = append(starts, start)
	}
	if len(starts) == 0 {
		return nil, steerBatch{}, 0, fmt.Errorf("%w: child-start Signal is missing", ErrInvalidExecutionState)
	}
	return starts, steer, uint32(len(signals)), nil
}

func collectChildWaitOpened(signals []run.Signal) (run.ChildWaitOpened, steerBatch, uint32, error) {
	var opened run.ChildWaitOpened
	var steer steerBatch
	for index, signal := range signals {
		frame, err := signal.Frame[run.SignalFrame]()
		if err != nil {
			return run.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: invalid wait opening: %w", ErrInvalidExecutionState, err)
		}
		switch body := frame.(type) {
		case run.ExternalInput:
			if err := steer.collectInput(signal, body); err != nil {
				return run.ChildWaitOpened{}, steerBatch{}, 0, err
			}
		case run.ChildWaitOpened:
			if opened.Valid() {
				return run.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: duplicate child wait opening", ErrInvalidExecutionState)
			}
			opened = body
		case run.ChildWaitSatisfied:
			if !opened.Valid() || opened.WaitID() != body.WaitID() {
				return run.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: completion does not match the child wait opening", ErrInvalidExecutionState)
			}
			return opened, steer, uint32(index), nil
		default:
			return run.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: unexpected %T while opening a child wait", ErrInvalidExecutionState, frame)
		}
	}
	if !opened.Valid() {
		return run.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: child wait opening is missing", ErrInvalidExecutionState)
	}
	return opened, steer, uint32(len(signals)), nil
}

func collectChildWaitSatisfied(signals []run.Signal) (run.ChildWaitSatisfied, steerBatch, uint32, error) {
	var completed run.ChildWaitSatisfied
	var found bool
	var steer steerBatch
	for _, signal := range signals {
		if recognized, err := steer.collectSignal(signal); err != nil {
			return run.ChildWaitSatisfied{}, steerBatch{}, 0, err
		} else if recognized {
			continue
		}
		value, err := signal.Frame[run.ChildWaitSatisfied]()
		if err != nil {
			return run.ChildWaitSatisfied{}, steerBatch{}, 0, fmt.Errorf("%w: invalid child completion Signal: %w", ErrInvalidExecutionState, err)
		}
		if found {
			return run.ChildWaitSatisfied{}, steerBatch{}, 0, fmt.Errorf("%w: duplicate child completion Signal", ErrInvalidExecutionState)
		}
		completed, found = value, true
	}
	if !found {
		return run.ChildWaitSatisfied{}, steerBatch{}, 0, fmt.Errorf("%w: child completion Signal is missing", ErrInvalidExecutionState)
	}
	return completed, steer, uint32(len(signals)), nil
}
