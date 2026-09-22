package coordination

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func BenchmarkRecordOutcomes(b *testing.B) {
	for _, count := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			starts, outcomes := competitionOutcomes(b, count)
			var prior, incoming []agent.ChildOutcome
			var indices []int
			for index, outcome := range outcomes {
				if index%2 == 0 {
					prior = append(prior, outcome)
				} else {
					incoming = append(incoming, outcome)
					indices = append(indices, index)
				}
			}
			state := firstSuccessState{Starts: starts}
			b.ReportAllocs()
			for b.Loop() {
				state.Outcomes = append(state.Outcomes[:0], prior...)
				state.recordOutcomes(indices, incoming)
			}
		})
	}
}

func competitionOutcomes(t testing.TB, count int) ([]agent.ChildStartResult, []agent.ChildOutcome) {
	t.Helper()
	schema, err := agent.SchemaFor[string]()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewInputGate(InputGateConfig{Name: "benchmark.gate", Description: "Measure outcome correlation.", RequestSchema: schema, AnswerSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{Definition: definition, ImplementationDigest: agent.ComputeDigest([]byte("implementation")), ConfigurationDigest: agent.ComputeDigest([]byte("configuration"))})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := jsonv2.Marshal(deployment.DeploymentRef())
	if err != nil {
		t.Fatal(err)
	}
	starts := make([]agent.ChildStartResult, count)
	outcomes := make([]agent.ChildOutcome, count)
	for index := range count {
		key := fmt.Sprintf("candidate_%04d", index)
		id := fmt.Sprintf("child_%04d", index)
		start := fmt.Sprintf(`{"operation":"start_child","key":%q,"deployment_ref":%s,"process_id":%q}`, key, ref, id)
		if err := jsonv2.Unmarshal([]byte(start), &starts[index]); err != nil {
			t.Fatal(err)
		}
		outcome := fmt.Sprintf(`{"boundary":"terminal_result","key":%q,"result":{"process_id":%q,"started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","output":7,"termination":{"status":"completed","cause":"completion"},"usage":{}}}`, key, id)
		if err := jsonv2.Unmarshal([]byte(outcome), &outcomes[index]); err != nil {
			t.Fatal(err)
		}
	}
	return starts, outcomes
}

func BenchmarkCompetitionRecovery(b *testing.B) {
	for _, count := range []int{64, 256, 1024} {
		starts, outcomes := competitionOutcomes(b, count)
		state := firstSuccessState{Phase: competitionCompleted, Starts: starts, Outcomes: outcomes}
		input, err := agent.EncodePayload("input")
		if err != nil {
			b.Fatal(err)
		}
		for _, start := range starts {
			state.Candidates = append(state.Candidates, agent.ChildSpec{Key: start.Key(), DeploymentRef: start.DeploymentRef(), Input: input})
		}
		definition, err := NewFirstSuccess(FirstSuccessConfig{Name: "benchmark.competition", Description: "Measure recovery.", MaxCandidates: uint32(count), Accept: func(context.Context, agent.ChildOutcome) (bool, error) { return false, nil }})
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
