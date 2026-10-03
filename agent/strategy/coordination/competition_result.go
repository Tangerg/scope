package coordination

import (
	agent "github.com/Tangerg/scope/agent"
)

// FirstSuccessResult preserves child-start facts and the terminal outcomes seen
// before selection. Both slices retain request order, so Starts[i] answers the
// i-th candidate. Children absent from Outcomes may still be settling when this
// result is published.
type FirstSuccessResult struct {
	// Winner is the accepted candidate's index in request order.
	Winner *uint32                  `json:"winner,omitzero"`
	Starts []agent.ChildStartResult `json:"starts"`
	// Outcomes is a non-nil ordered collection, including when every start failed.
	Outcomes []agent.ChildOutcome `json:"outcomes"`
}

func (f FirstSuccessResult) Valid() bool {
	if len(f.Starts) == 0 || f.Outcomes == nil {
		return false
	}
	// Outcomes follow the started candidates in request order.
	next := 0
	for _, outcome := range f.Outcomes {
		if !outcome.Valid() {
			return false
		}
		for next < len(f.Starts) && !f.started(next, outcome.Result().ProcessID()) {
			next++
		}
		if next == len(f.Starts) {
			return false
		}
		next++
	}
	for _, started := range f.Starts {
		if !started.Valid() {
			return false
		}
	}
	if f.Winner != nil {
		return f.winnerCompleted()
	}
	startedCount := 0
	for _, started := range f.Starts {
		if _, present := started.ProcessID(); present {
			startedCount++
		}
	}
	return startedCount == len(f.Outcomes)
}

func (f FirstSuccessResult) started(index int, id agent.ProcessID) bool {
	started, present := f.Starts[index].ProcessID()
	return present && started == id
}

func (f FirstSuccessResult) winnerCompleted() bool {
	if uint64(*f.Winner) >= uint64(len(f.Starts)) {
		return false
	}
	id, present := f.Starts[*f.Winner].ProcessID()
	if !present {
		return false
	}
	for _, outcome := range f.Outcomes {
		if outcome.Result().ProcessID() == id {
			return outcome.Result().Status() == agent.StatusCompleted
		}
	}
	return false
}
