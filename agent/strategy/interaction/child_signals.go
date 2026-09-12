package interaction

import (
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

func collectChildStarts(signals []agent.Signal) ([]agent.ChildStartResult, steerBatch, uint32, error) {
	starts := make([]agent.ChildStartResult, 0, len(signals))
	var steer steerBatch
	for _, signal := range signals {
		if recognized, err := steer.collectSignal(signal); err != nil {
			return nil, steerBatch{}, 0, err
		} else if recognized {
			continue
		}
		start, err := agent.ParseChildStartResult(signal)
		if err != nil {
			return nil, steerBatch{}, 0, fmt.Errorf("%w: invalid child-start Signal", ErrInvalidExecutionState)
		}
		starts = append(starts, start)
	}
	if len(starts) == 0 {
		return nil, steerBatch{}, 0, fmt.Errorf("%w: child-start Signal is missing", ErrInvalidExecutionState)
	}
	return starts, steer, uint32(len(signals)), nil
}

func collectChildWaitOpened(signals []agent.Signal) (agent.ChildWaitOpened, steerBatch, uint32, error) {
	var opened agent.ChildWaitOpened
	var found bool
	var steer steerBatch
	var consumed uint32
	for _, signal := range signals {
		if recognized, err := steer.collectSignal(signal); err != nil {
			return agent.ChildWaitOpened{}, steerBatch{}, 0, err
		} else if recognized {
			consumed++
			continue
		}
		value, err := agent.ParseChildWaitOpened(signal)
		if err == nil {
			if found {
				return agent.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: duplicate child wait-opened Signal", ErrInvalidExecutionState)
			}
			opened, found = value, true
			consumed++
			continue
		}
		if found {
			if _, completionErr := agent.ParseChildWaitSatisfied(signal); completionErr == nil {
				break
			}
		}
		return agent.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: invalid child wait-opened Signal", ErrInvalidExecutionState)
	}
	if !found {
		return agent.ChildWaitOpened{}, steerBatch{}, 0, fmt.Errorf("%w: child wait-opened Signal is missing", ErrInvalidExecutionState)
	}
	return opened, steer, consumed, nil
}

func collectChildWaitSatisfied(signals []agent.Signal) (agent.ChildWaitSatisfied, steerBatch, uint32, error) {
	var completed agent.ChildWaitSatisfied
	var found bool
	var steer steerBatch
	for _, signal := range signals {
		if recognized, err := steer.collectSignal(signal); err != nil {
			return agent.ChildWaitSatisfied{}, steerBatch{}, 0, err
		} else if recognized {
			continue
		}
		value, err := agent.ParseChildWaitSatisfied(signal)
		if err != nil || found {
			return agent.ChildWaitSatisfied{}, steerBatch{}, 0, fmt.Errorf("%w: invalid or duplicate child completion Signal", ErrInvalidExecutionState)
		}
		completed, found = value, true
	}
	if !found {
		return agent.ChildWaitSatisfied{}, steerBatch{}, 0, fmt.Errorf("%w: child completion Signal is missing", ErrInvalidExecutionState)
	}
	return completed, steer, uint32(len(signals)), nil
}
