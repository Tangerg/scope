package coordination

import agent "github.com/Tangerg/scope/agent"

// FirstSuccessResult preserves child-start facts and the terminal outcomes seen
// before selection. Both slices retain request order. Children absent from
// Outcomes may still be settling when this result is published.
type FirstSuccessResult struct {
	Winner   *agent.ChildKey          `json:"winner,omitempty"`
	Starts   []agent.ChildStartResult `json:"starts"`
	Outcomes []agent.ChildOutcome     `json:"outcomes"`
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
	previous := -1
	for _, outcome := range outcomes {
		index := matchingStart(starts, outcome)
		if index <= previous {
			return false
		}
		previous = index
	}
	return true
}

func matchingStart(starts []agent.ChildStartResult, outcome agent.ChildOutcome) int {
	if !outcome.Valid() {
		return -1
	}
	for index, started := range starts {
		id, present := started.ProcessID()
		if present && started.Key() == outcome.Key() && id == outcome.Result().ProcessID() {
			return index
		}
	}
	return -1
}

func observedProcess(outcomes []agent.ChildOutcome, processID agent.ProcessID) bool {
	for _, outcome := range outcomes {
		if outcome.Result().ProcessID() == processID {
			return true
		}
	}
	return false
}
