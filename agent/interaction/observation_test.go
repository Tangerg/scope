package interaction

import (
	"errors"
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
			var outcome run.Settlement
			if row.status == run.SettlementStatusFailed {
				failure := mustValue(run.NewFailure(run.FailureKindContract, failureCodeDispatchRejected, "rejected"))
				outcome = mustValue(run.NewFailedSettlement(failure, mustValue(run.ParsePayload([]byte(row.wire)))))
			} else {
				outcome = mustValue(run.NewSettlement(row.status, mustValue(run.ParsePayload([]byte(row.wire)))))
			}
			var err error
			if row.model {
				_, err = ModelSettlementFromRecord(run.EffectSettled{Outcome: outcome})
			} else {
				_, err = ToolSettlementFromRecord(requested("tool", "call"), run.EffectSettled{Outcome: outcome})
			}
			if err == nil {
				t.Fatal("contradictory evidence was admitted")
			}
		})
	}
}

// When Dispatch returns an error the runtime records Unknown with its own
// Failure and a null payload; the projections own that committed shape too.
func TestSettlementProjectionsReadRuntimeRecordedUncertainty(t *testing.T) {
	failure := mustValue(run.NewFailure(run.FailureKindExternal, "runtime.dispatch.failed", "connection reset"))
	outcome := mustValue(run.NewSettlement(run.SettlementStatusUnknown, mustValue(run.ParsePayload([]byte(`null`)))))
	record := run.EffectSettled{Outcome: outcome, Failure: &failure}
	model := mustValue(ModelSettlementFromRecord(record))
	if model.Outcome != ModelOutcomeUnknown || model.Failure != "connection reset" {
		t.Fatalf("model settlement = %+v", model)
	}
	tool := mustValue(ToolSettlementFromRecord(requested("tool", "call"), record))
	if tool.Outcome != ToolOutcomeUnknown || tool.Failure != "connection reset" {
		t.Fatalf("tool settlement = %+v", tool)
	}
	succeeded := mustValue(run.NewSettlement(run.SettlementStatusSucceeded, mustValue(run.ParsePayload([]byte(`{}`)))))
	if _, err := ModelSettlementFromRecord(run.EffectSettled{Outcome: succeeded, Failure: &failure}); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatalf("runtime failure beside a definite outcome: %v", err)
	}
}
