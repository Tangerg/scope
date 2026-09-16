package coordination

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func BenchmarkRecordOutcomes(b *testing.B) {
	for _, count := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			starts, outcomes := competitionOutcomes(b, count)
			var prior, incoming []agent.ChildOutcome
			for index, outcome := range outcomes {
				if index%2 == 0 {
					prior = append(prior, outcome)
				} else {
					incoming = append(incoming, outcome)
				}
			}
			state := firstSuccessState{Starts: starts}
			b.ReportAllocs()
			for b.Loop() {
				state.Outcomes = append(state.Outcomes[:0], prior...)
				if err := state.recordOutcomes(incoming); err != nil {
					b.Fatal(err)
				}
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
	ref, err := json.Marshal(deployment.DeploymentRef())
	if err != nil {
		t.Fatal(err)
	}
	starts := make([]agent.ChildStartResult, count)
	outcomes := make([]agent.ChildOutcome, count)
	for index := range count {
		key := fmt.Sprintf("candidate_%04d", index)
		id := fmt.Sprintf("child_%04d", index)
		start := fmt.Sprintf(`{"operation":"start_child","key":%q,"deployment_ref":%s,"process_id":%q}`, key, ref, id)
		if err := json.Unmarshal([]byte(start), &starts[index]); err != nil {
			t.Fatal(err)
		}
		outcome := fmt.Sprintf(`{"boundary":"terminal_result","key":%q,"result":{"process_id":%q,"started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","output":7,"termination":{"status":"completed","cause":"completion"},"usage":{}}}`, key, id)
		if err := json.Unmarshal([]byte(outcome), &outcomes[index]); err != nil {
			t.Fatal(err)
		}
	}
	return starts, outcomes
}
