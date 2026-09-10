package coordination

import (
	"cmp"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
)

type competitionPhase string

const (
	competitionReady          competitionPhase = "ready"
	competitionAwaitingStarts competitionPhase = "awaiting_starts"
	competitionAwaitingOpen   competitionPhase = "awaiting_open"
	competitionWaiting        competitionPhase = "waiting"
	competitionCompleted      competitionPhase = "completed"
)

type firstSuccessState struct {
	Phase      competitionPhase         `json:"phase"`
	Candidates []agent.ChildSpec        `json:"candidates"`
	Starts     []agent.ChildStartResult `json:"starts,omitempty"`
	Outcomes   []agent.ChildOutcome     `json:"outcomes,omitempty"`
	WaitID     *agent.WaitID            `json:"wait_id,omitempty"`
	Winner     *agent.ChildKey          `json:"winner,omitempty"`
}

func (f firstSuccessState) validate(maxCandidates uint32) error {
	if len(f.Candidates) == 0 || uint64(len(f.Candidates)) > uint64(maxCandidates) || len(f.Starts) > len(f.Candidates) {
		return fmt.Errorf("%w: candidate or start count exceeds its bound", ErrInvalidState)
	}
	for index, candidate := range f.Candidates {
		if !candidate.Valid() {
			return fmt.Errorf("%w: invalid candidate", ErrInvalidState)
		}
		for _, previous := range f.Candidates[:index] {
			if candidate.Key == previous.Key {
				return fmt.Errorf("%w: duplicate candidate key", ErrInvalidState)
			}
		}
	}
	for index, started := range f.Starts {
		candidate := f.Candidates[index]
		if !started.Valid() || started.Key() != candidate.Key || started.DeploymentRef() != candidate.DeploymentRef {
			return fmt.Errorf("%w: start fact disagrees with its candidate", ErrInvalidState)
		}
		if id, present := started.ProcessID(); present {
			for _, previous := range f.Starts[:index] {
				if previousID, present := previous.ProcessID(); present && previousID == id {
					return fmt.Errorf("%w: duplicate candidate ProcessID", ErrInvalidState)
				}
			}
		}
	}
	if !validObservedOutcomes(f.Starts, f.Outcomes) {
		return fmt.Errorf("%w: observed outcomes disagree with candidate order or identities", ErrInvalidState)
	}
	if f.Phase != competitionCompleted && f.Winner != nil {
		return fmt.Errorf("%w: unfinished competition contains a winner", ErrInvalidState)
	}
	switch f.Phase {
	case competitionReady:
		if len(f.Starts) == 0 && len(f.Outcomes) == 0 && f.WaitID == nil {
			return nil
		}
	case competitionAwaitingStarts:
		if len(f.Starts) < len(f.Candidates) && len(f.Outcomes) == 0 && f.WaitID == nil {
			return nil
		}
	case competitionAwaitingOpen, competitionWaiting:
		validWait := f.WaitID == nil
		if f.Phase == competitionWaiting {
			validWait = f.WaitID != nil && f.WaitID.Valid()
		}
		if validWait && len(f.Starts) == len(f.Candidates) && len(f.remaining()) != 0 {
			return nil
		}
	case competitionCompleted:
		if len(f.Starts) == len(f.Candidates) && f.WaitID == nil && f.result().Valid() {
			return nil
		}
	}
	return fmt.Errorf("%w: competition phase disagrees with its progress", ErrInvalidState)
}

func (f firstSuccessState) remaining() []agent.ProcessID {
	var children []agent.ProcessID
	for _, started := range f.Starts {
		if id, present := started.ProcessID(); present && !observedProcess(f.Outcomes, id) {
			children = append(children, id)
		}
	}
	return children
}

func (f firstSuccessState) waitSpec() (agent.ChildWaitSpec, error) {
	key, err := agent.ParseWaitKey(fmt.Sprintf("coordination.first_success.%d", len(f.Outcomes)))
	if err != nil {
		return agent.ChildWaitSpec{}, err
	}
	spec := agent.ChildWaitSpec{
		Key: key, Children: f.remaining(), Boundary: agent.ChildWaitBoundaryResult, Condition: agent.AnyChild(),
	}
	if !spec.Valid() {
		return agent.ChildWaitSpec{}, fmt.Errorf("%w: competition has no remaining child to wait for", ErrInvalidState)
	}
	return spec, nil
}

func (f *firstSuccessState) recordOutcomes(outcomes []agent.ChildOutcome) error {
	if len(outcomes) == 0 || !validObservedOutcomes(f.Starts, outcomes) {
		return fmt.Errorf("%w: candidate outcomes are empty, unordered, or foreign", ErrInvalidProtocol)
	}
	for _, outcome := range outcomes {
		if observedProcess(f.Outcomes, outcome.Result().ProcessID()) {
			return fmt.Errorf("%w: candidate outcome was already observed", ErrInvalidProtocol)
		}
	}
	f.Outcomes = append(f.Outcomes, outcomes...)
	slices.SortFunc(f.Outcomes, func(left, right agent.ChildOutcome) int {
		return cmp.Compare(matchingStart(f.Starts, left), matchingStart(f.Starts, right))
	})
	return nil
}

func (f firstSuccessState) result() FirstSuccessResult {
	result := FirstSuccessResult{
		Starts: slices.Clone(f.Starts), Outcomes: append([]agent.ChildOutcome{}, f.Outcomes...),
	}
	if f.Winner != nil {
		winner := *f.Winner
		result.Winner = &winner
	}
	return result
}

func sameWaitSpec(left, right agent.ChildWaitSpec) bool {
	return left.Key == right.Key && left.Boundary == right.Boundary && left.Condition == right.Condition &&
		slices.Equal(left.Children, right.Children)
}
