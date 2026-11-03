package interaction

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
)

func TestModelSettlementKeepsDefiniteFailureEvidence(t *testing.T) {
	for _, test := range []struct {
		name    string
		cause   error
		outcome ModelOutcome
	}{
		{name: "not executed", cause: &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: errors.New("request rejected")}, outcome: ModelOutcomeNotExecuted},
		{name: "definite failure", cause: &invocation.Failure{Kind: invocation.FailureFailed, Cause: errors.New("provider refused completion")}, outcome: ModelOutcomeFailed},
		{name: "unknown", cause: errors.New("lost response"), outcome: ModelOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			settlement := mustValue(ModelSettlementFromOutcome(mustValue(modelFailureSettlement(nil, test.cause))))
			if settlement.Outcome != test.outcome || settlement.Failure != test.cause.Error() || settlement.Response != nil || settlement.Validate() != nil {
				t.Fatalf("settlement=%+v, want %s", settlement, test.outcome)
			}
		})
	}
}

func TestFailedSettlementKeepsCompletedGenerationAcrossPersistence(t *testing.T) {
	response := finalResponse("generated before commit failed")
	response.Metadata = &chat.ResponseMetadata{Usage: &chat.Usage{InputTokens: 17, OutputTokens: 5}}
	cause := &invocation.Failure{Kind: invocation.FailureFailed, Cause: errors.New("history commit rejected")}
	settled := mustValue(modelFailureSettlement(response, cause))
	failure, found := settled.Failure()
	if !found || failure.Message() != cause.Error() || settled.Status() != run.SettlementStatusFailed {
		t.Fatal("definite failure diagnostic escaped its run owner")
	}
	encoded := mustValue(jsonv2.Marshal(settled))
	var restored run.Settlement
	if err := jsonv2.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	response.Metadata.Usage.InputTokens = 99
	projected := mustValue(ModelSettlementFromOutcome(restored))
	if projected.Outcome != ModelOutcomeFailed || projected.Failure != failure.Message() || projected.Response.Output.Text() != "generated before commit failed" || projected.Response.Metadata.Usage.TotalTokens() != 22 {
		t.Fatalf("completed work was lost or advanced separately: %+v", projected)
	}
	if _, err := ToolSettlementFromOutcome(requested("tool", "call"), restored); err == nil {
		t.Fatal("tool projection admitted model-owned generation")
	}
}

func TestModelSettlementRejectsCompetingOrMissingEvidence(t *testing.T) {
	response := &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}}
	for _, test := range []struct {
		name       string
		settlement ModelSettlement
		valid      bool
	}{
		{name: "response", settlement: ModelSettlement{Outcome: ModelOutcomeSucceeded, Response: response}, valid: true},
		{name: "unknown without diagnostic", settlement: ModelSettlement{Outcome: ModelOutcomeUnknown}, valid: true},
		{name: "generation before failed postprocessing", settlement: ModelSettlement{Outcome: ModelOutcomeFailed, Response: response, Failure: "commit rejected"}, valid: true},
		{name: "generation before uncertain postprocessing", settlement: ModelSettlement{Outcome: ModelOutcomeUnknown, Response: response, Failure: "commit unknown"}, valid: true},
		{name: "missing outcome", settlement: ModelSettlement{Response: response}},
		{name: "unobserved is not a settlement", settlement: ModelSettlement{Outcome: ModelOutcome("unobserved")}},
		{name: "success without response", settlement: ModelSettlement{Outcome: ModelOutcomeSucceeded}},
		{name: "success with failure", settlement: ModelSettlement{Outcome: ModelOutcomeSucceeded, Response: response, Failure: "failed"}},
		{name: "invalid response", settlement: ModelSettlement{Outcome: ModelOutcomeSucceeded, Response: &chat.Response{}}},
		{name: "invalid failure response", settlement: ModelSettlement{Outcome: ModelOutcomeFailed, Response: &chat.Response{}, Failure: "failed"}},
		{name: "failure without diagnostic", settlement: ModelSettlement{Outcome: ModelOutcomeFailed}},
		{name: "not executed with response", settlement: ModelSettlement{Outcome: ModelOutcomeNotExecuted, Response: response, Failure: "rejected"}},
		{name: "unknown with response", settlement: ModelSettlement{Outcome: ModelOutcomeUnknown, Response: response}},
		{name: "untrimmed diagnostic", settlement: ModelSettlement{Outcome: ModelOutcomeUnknown, Failure: " failed "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.settlement.Validate()
			if test.valid && err != nil || !test.valid && !errors.Is(err, ErrInvalidModelSettlement) {
				t.Fatalf("settlement=%+v validation=%v", test.settlement, err)
			}
		})
	}
}
