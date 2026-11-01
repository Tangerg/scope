package coordination

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func BenchmarkRecordOutcomes(b *testing.B) {
	for _, count := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			starts, outcomes := competitionOutcomes(b, count)
			var prior, incoming []run.ChildOutcome
			var indices []int
			for index, outcome := range outcomes {
				if index%2 == 0 {
					prior = append(prior, outcome)
				} else {
					incoming = append(incoming, outcome)
					indices = append(indices, index)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				state := firstSuccessState{Results: startSlots(starts)}
				for index := 0; index < len(prior); index++ {
					state.Results[2*index] = &CandidateResult{Outcome: &prior[index]}
				}
				state.recordOutcomes(indices, incoming)
			}
		})
	}
}

func benchmarkProgramRef(t testing.TB) run.ProgramRef {
	t.Helper()
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewInputGate(InputGateConfig{Name: "benchmark.gate", Description: "Measure outcome correlation.", RequestSchema: schema, AnswerSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{Definition: definition, ImplementationDigest: run.ComputeDigest([]byte("implementation")), ConfigurationDigest: run.ComputeDigest([]byte("configuration"))})
	if err != nil {
		t.Fatal(err)
	}
	return deployment.ProgramRef()
}

func candidateKey(t testing.TB, index int) run.ChildKey {
	t.Helper()
	key, err := run.ParseChildKey(fmt.Sprintf("candidate_%04d", index))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func competitionOutcomes(t testing.TB, count int) ([]run.ChildStartResult, []run.ChildOutcome) {
	t.Helper()
	starts := make([]run.ChildStartResult, count)
	outcomes := make([]run.ChildOutcome, count)
	for index := range count {
		id := fmt.Sprintf("child_%04d", index)
		start := fmt.Sprintf(`{"operation":"start_child","process_id":%q}`, id)
		if err := jsonv2.Unmarshal([]byte(start), &starts[index]); err != nil {
			t.Fatal(err)
		}
		outcome := fmt.Sprintf(`{"result":{"process_id":%q,"started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","termination":{"output":7},"usage":{"committed_steps":0,"prepared_effects":0,"accepted_signals":0,"dropped_deltas":0}}}`, id)
		if err := jsonv2.Unmarshal([]byte(outcome), &outcomes[index]); err != nil {
			t.Fatal(err)
		}
	}
	return starts, outcomes
}

func BenchmarkCompetitionRecovery(b *testing.B) {
	for _, count := range []int{64, 256, 1024} {
		starts, outcomes := competitionOutcomes(b, count)
		state := firstSuccessState{Results: startSlots(starts)}
		for index := range outcomes[:len(outcomes)-1] {
			state.Results[index] = &CandidateResult{Outcome: &outcomes[index]}
		}
		input, err := run.EncodePayload("input")
		if err != nil {
			b.Fatal(err)
		}
		for index := range starts {
			state.Candidates = append(state.Candidates, run.ChildSpec{Key: candidateKey(b, index), ProgramRef: benchmarkProgramRef(b), Input: input})
		}
		definition, err := NewFirstSuccess(FirstSuccessConfig{Name: "benchmark.competition", Description: "Measure recovery.", MaxCandidates: uint32(count), Accept: func(context.Context, run.ChildKey, run.ChildOutcome) (bool, error) { return false, nil }})
		if err != nil {
			b.Fatal(err)
		}
		execution := &firstSuccessExecution{definition: definition, state: state}
		snapshot, err := execution.Snapshot()
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("validate/%d", count), func(b *testing.B) {
			for b.Loop() {
				if err := state.validate(b.Context(), uint32(count)); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("restore/%d", count), func(b *testing.B) {
			for b.Loop() {
				if _, err := definition.Restore(b.Context(), snapshot); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
