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
	batch := childcall.Batch{Children: make([]childcall.Child, len(f.Starts))}
	for index, started := range f.Starts {
		if !started.Valid() {
			return false
		}
		id, present := started.ProcessID()
		batch.Children[index] = childcall.Child{Key: started.Key(), ProcessID: id, Done: !present}
	}
	if _, err := batch.MatchOutcomes(f.Outcomes); err != nil {
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

func observedProcess(outcomes []agent.ChildOutcome, processID agent.ProcessID) bool {
	for _, outcome := range outcomes {
		if outcome.Result().ProcessID() == processID {
			return true
		}
	}
	return false
}
