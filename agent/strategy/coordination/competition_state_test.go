package coordination

import (
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func TestCompetitionMergesOutcomesInCandidateOrder(t *testing.T) {
	starts, outcomes := competitionOutcomes(t, 6)
	state := firstSuccessState{Starts: starts}
	for _, batch := range [][]agent.ChildOutcome{
		{outcomes[1], outcomes[3], outcomes[5]},
		{outcomes[0], outcomes[2], outcomes[4]},
	} {
		if err := state.recordOutcomes(batch); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.EqualFunc(state.Outcomes, outcomes, sameOutcome) {
		t.Fatal("merged outcomes changed candidate order or contents")
	}
	if !state.result().Valid() {
		t.Fatal("complete competition result is invalid")
	}
}

func TestCompetitionRejectsInvalidOutcomeBatchesAtomically(t *testing.T) {
	starts, outcomes := competitionOutcomes(t, 4)
	for name, batch := range map[string][]agent.ChildOutcome{
		"empty":            nil,
		"duplicate":        {outcomes[0], outcomes[0]},
		"already observed": {outcomes[0], outcomes[1]},
		"unordered":        {outcomes[2], outcomes[0]},
		"foreign":          {outcomes[0], outcomes[3]},
		"invalid":          {{}},
	} {
		t.Run(name, func(t *testing.T) {
			state := firstSuccessState{Starts: starts[:3], Outcomes: []agent.ChildOutcome{outcomes[1]}}
			before := slices.Clone(state.Outcomes)
			if err := state.recordOutcomes(batch); !errors.Is(err, ErrInvalidProtocol) {
				t.Fatalf("recordOutcomes error = %v", err)
			}
			if !slices.EqualFunc(state.Outcomes, before, sameOutcome) {
				t.Fatal("rejected batch changed observed outcomes")
			}
		})
	}
}

func sameOutcome(left, right agent.ChildOutcome) bool {
	return left.Key() == right.Key() && left.Result().ProcessID() == right.Result().ProcessID() &&
		left.Result().Status() == right.Result().Status()
}

func TestCompetitionRejectsUnmergeableRetainedOutcomes(t *testing.T) {
	starts, outcomes := competitionOutcomes(t, 3)
	state := firstSuccessState{Starts: starts[:2], Outcomes: []agent.ChildOutcome{outcomes[2]}}
	if err := state.recordOutcomes(outcomes[:1]); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatalf("unmergeable retained outcome was silently discarded: %v", err)
	}
	if len(state.Outcomes) != 1 || !sameOutcome(state.Outcomes[0], outcomes[2]) {
		t.Fatal("failed merge changed retained evidence")
	}
}
