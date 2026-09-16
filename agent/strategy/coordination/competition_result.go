package coordination

import (
	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

// FirstSuccessResult preserves child-start facts and the terminal outcomes seen
// before selection. Both slices retain request order. Children absent from
// Outcomes may still be settling when this result is published.
type FirstSuccessResult struct {
	Winner *agent.ChildKey          `json:"winner,omitempty"`
	Starts []agent.ChildStartResult `json:"starts"`
	// Outcomes is a non-nil ordered collection, including when every start failed.
	Outcomes []agent.ChildOutcome `json:"outcomes"`
}

func (f FirstSuccessResult) Valid() bool {
	if len(f.Starts) == 0 || f.Outcomes == nil {
		return false
	}
	for index, started := range f.Starts {
		if !started.Valid() {
			return false
		}
		id, present := started.ProcessID()
		for _, previous := range f.Starts[:index] {
			previousID, previousPresent := previous.ProcessID()
			if previous.Key() == started.Key() || present && previousPresent && previousID == id {
				return false
			}
		}
	}
	if !validObservedOutcomes(f.Starts, f.Outcomes) {
		return false
	}
	if f.Winner != nil {
		for _, outcome := range f.Outcomes {
			if outcome.Key() == *f.Winner {
				return outcome.Result().Status() == agent.StatusCompleted
			}
		}
		return false
	}
	for _, started := range f.Starts {
		if id, present := started.ProcessID(); present && !observedProcess(f.Outcomes, id) {
			return false
		}
	}
	return true
}

func validObservedOutcomes(starts []agent.ChildStartResult, outcomes []agent.ChildOutcome) bool {
	next := 0
	for _, outcome := range outcomes {
		if !outcome.Valid() {
			return false
		}
		for next < len(starts) && !outcomeMatchesStart(outcome, starts[next]) {
			next++
		}
		if next == len(starts) {
			return false
		}
		next++
	}
	return true
}

func outcomeMatchesStart(outcome agent.ChildOutcome, start agent.ChildStartResult) bool {
	id, present := start.ProcessID()
	return present && childcall.OutcomeMatches(outcome, start.Key(), id)
}

func observedProcess(outcomes []agent.ChildOutcome, processID agent.ProcessID) bool {
	for _, outcome := range outcomes {
		if outcome.Result().ProcessID() == processID {
			return true
		}
	}
	return false
}
