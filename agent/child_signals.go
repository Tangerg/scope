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
		start, err := run.ParseChildStartResult(signal)
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
	var found bool
	var steer steerBatch
	var consumed uint32
	for _, signal := range signals {
		if recognized, err := steer.collectSignal(signal); err != nil {
			return run.ChildWaitOpened{}, steerBatch{}, 0, err
		} else if recognized {
			consumed++
			continue
		}
		value, err := run.ParseChildWaitOpened(signal)
		if err == nil {
			if found {
				return run.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: duplicate child wait-opened Signal", ErrInvalidExecutionState)
			}
			opened, found = value, true
			consumed++
			continue
		}
		if found {
			if _, completionErr := run.ParseChildWaitSatisfied(signal); completionErr == nil {
				break
			}
		}
		return run.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: invalid child wait-opened Signal: %w", ErrInvalidExecutionState, err)
	}
	if !found {
		return run.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: child wait-opened Signal is missing", ErrInvalidExecutionState)
	}
	return opened, steer, consumed, nil
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
		value, err := run.ParseChildWaitSatisfied(signal)
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
