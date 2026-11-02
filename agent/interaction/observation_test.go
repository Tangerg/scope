package interaction

import (
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestCommittedEvidenceRejectsContradictoryOpaqueOutcomes(t *testing.T) {
	for _, row := range []struct {
		name, wire string
		status     run.SettlementStatus
		model      bool
	}{
		{name: "unknown cannot prove nonexecution", wire: `{"model":{"diagnostic":"lost","not_executed":true}}`, status: run.SettlementStatusUnknown, model: true},
		{name: "tool error cannot add a final result", wire: `{"tool":{"diagnostic":"lost","result":{"id":"call","name":"tool"}}}`, status: run.SettlementStatusUnknown},
		{name: "tool error cannot add an input question", wire: `{"tool":{"diagnostic":"lost","input_required":true}}`, status: run.SettlementStatusUnknown},
		{name: "tool evidence cannot settle definite failure", wire: `{"tool":{"diagnostic":"lost"}}`, status: run.SettlementStatusFailed},
		{name: "malformed evidence", wire: `{"tool":{"diagnostic":"lost","evidence":{"contents":[{"type":"unknown"}]}}}`, status: run.SettlementStatusUnknown},
		{name: "competing evidence variants", wire: `{"diagnostic":"lost","model":{"diagnostic":"lost"}}`, status: run.SettlementStatusUnknown, model: true},
		{name: "removed model outcome discriminator", wire: `{"model":{"diagnostic":"lost","outcome":"succeeded"}}`, status: run.SettlementStatusUnknown, model: true},
		{name: "model cannot decode tool evidence", wire: `{"tool":{"diagnostic":"lost"}}`, status: run.SettlementStatusUnknown, model: true},
		{name: "tool cannot decode model evidence", wire: `{"model":{"diagnostic":"lost"}}`, status: run.SettlementStatusUnknown},
	} {
		t.Run(row.name, func(t *testing.T) {
			outcome := mustValue(run.NewSettlement(row.status, []byte(row.wire)))
			var err error
			if row.model {
				_, err = ModelSettlementFromOutcome(outcome)
			} else {
				_, err = ToolSettlementFromOutcome(requested("tool", "call"), outcome)
			}
			if err == nil {
				t.Fatal("contradictory evidence was admitted")
			}
		})
	}
}
