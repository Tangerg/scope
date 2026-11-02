package coordination

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

type firstSuccessState struct {
	Candidates    []run.ChildSpec    `json:"candidates,omitempty"`
	Calls         []child.Invocation `json:"calls,omitempty"`
	Winner        *uint32            `json:"winner,omitzero"`
	Cancellations []run.EffectID     `json:"cancellations,omitempty"`
	Completed     bool               `json:"completed,omitzero"`
}

func (f firstSuccessState) validate(ctx context.Context, maximum uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.Completed {
		if len(f.Candidates)+len(f.Calls)+len(f.Cancellations) != 0 || f.Winner != nil {
			return ErrInvalidExecutionState
		}
		return nil
	}
	if len(f.Candidates) == 0 || uint64(len(f.Candidates)) > uint64(maximum) || len(f.Calls) != 0 && len(f.Calls) != len(f.Candidates) {
		return ErrInvalidExecutionState
	}
	keys := make(map[run.ChildKey]struct{}, len(f.Candidates))
	for _, candidate := range f.Candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !candidate.Valid() {
			return ErrInvalidExecutionState
		}
		if _, exists := keys[candidate.Key]; exists {
			return fmt.Errorf("%w: duplicate candidate key", ErrInvalidExecutionState)
		}
		keys[candidate.Key] = struct{}{}
	}
	parent, err := f.validateCalls()
	if err != nil {
		return err
	}
	if f.Winner != nil && (!f.admitted() || uint64(*f.Winner) >= uint64(len(f.Calls)) || f.Calls[*f.Winner].Termination == nil || f.Calls[*f.Winner].Termination.Status() != run.StatusCompleted) {
		return ErrInvalidExecutionState
	}
	return f.validateCancellations(parent)
}

func (f firstSuccessState) validateCalls() (run.ProcessID, error) {
	var parent run.ProcessID
	var started uint64
	for index, call := range f.Calls {
		if !call.Valid() {
			return run.ProcessID{}, ErrInvalidExecutionState
		}
		var current run.ProcessID
		switch {
		case call.Request != nil:
			if call.Request.Index != uint32(index) || started != 0 && call.Request.Step != started {
				return run.ProcessID{}, ErrInvalidExecutionState
			}
			current, started = call.Request.Process, call.Request.Step
		case call.Process != nil:
			key, _ := call.Process.Key()
			if key != f.Candidates[index].Key {
				return run.ProcessID{}, ErrInvalidExecutionState
			}
			current, _ = call.Process.Parent()
		default:
			continue
		}
		if parent.Valid() && parent != current {
			return run.ProcessID{}, ErrInvalidExecutionState
		}
		parent = current
	}
	return parent, nil
}

func (f firstSuccessState) validateCancellations(parent run.ProcessID) error {
	if len(f.Cancellations) != 0 && (f.Winner == nil || len(f.Cancellations) >= len(f.Calls)) {
		return ErrInvalidExecutionState
	}
	var previous run.EffectID
	for _, id := range f.Cancellations {
		if !id.Valid() || parent.Valid() && id.Process != parent || uint64(id.Index)+1 >= uint64(len(f.Calls)) {
			return ErrInvalidExecutionState
		}
		if previous.Valid() && (previous.Process != id.Process || previous.Step != id.Step || previous.Index >= id.Index) {
			return ErrInvalidExecutionState
		}
		previous = id
	}
	return nil
}

func (f firstSuccessState) drained() bool {
	if len(f.Calls) == 0 || len(f.Cancellations) != 0 {
		return false
	}
	for _, call := range f.Calls {
		if !call.Done() {
			return false
		}
	}
	return true
}

func (f firstSuccessState) admitted() bool {
	for _, call := range f.Calls {
		if call.Request != nil {
			return false
		}
	}
	return len(f.Calls) > 0
}

func (f firstSuccessState) result() FirstSuccessResult {
	result := FirstSuccessResult{Winner: f.Winner, Candidates: make([]CandidateResult, len(f.Calls))}
	for index, call := range f.Calls {
		result.Candidates[index] = CandidateResult{Outcome: call.Termination, Rejection: call.Rejection}
	}
	return result
}
