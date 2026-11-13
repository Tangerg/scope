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
}

func (f firstSuccessState) validate(ctx context.Context, maximum uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.validateCandidates(maximum); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if len(f.Calls) != 0 && len(f.Calls) != len(f.Candidates) {
		return ErrInvalidExecutionState
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

// validateCandidates is the input rule: Start applies it before any child
// exists, and Restore applies it again beneath the progress built on it.
func (f firstSuccessState) validateCandidates(maximum uint32) error {
	if len(f.Candidates) == 0 || uint64(len(f.Candidates)) > uint64(maximum) {
		return fmt.Errorf("competition requires between 1 and %d candidates", maximum)
	}
	keys := make(map[run.ChildKey]struct{}, len(f.Candidates))
	for _, candidate := range f.Candidates {
		if _, exists := keys[candidate.Key]; exists {
			return fmt.Errorf("duplicate candidate key %q", candidate.Key)
		}
		keys[candidate.Key] = struct{}{}
	}
	return nil
}

func (f firstSuccessState) validateCalls() (run.ProcessID, error) {
	parent, err := child.Batch(f.Calls).Parent(func(position int) (run.ChildKey, error) { return f.Candidates[position].Key, nil })
	if err != nil {
		return run.ProcessID{}, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	return parent, nil
}

func (f firstSuccessState) validateCancellations(parent run.ProcessID) error {
	if len(f.Cancellations) != 0 && (f.Winner == nil || len(f.Cancellations) >= len(f.Calls)) {
		return ErrInvalidExecutionState
	}
	var previous run.EffectID
	for _, id := range f.Cancellations {
		if parent.Valid() && id.Process != parent || uint64(id.Index)+1 >= uint64(len(f.Calls)) {
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
