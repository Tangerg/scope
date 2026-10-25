package interaction

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestModelSettlementKeepsDefiniteFailureEvidence(t *testing.T) {
	for _, test := range []struct {
		name    string
		cause   error
		outcome ModelOutcome
	}{
		{name: "not executed", cause: &chat.CallFailure{Kind: chat.CallFailureNotExecuted, Cause: errors.New("request rejected")}, outcome: ModelOutcomeNotExecuted},
		{name: "definite failure", cause: &chat.CallFailure{Kind: chat.CallFailureFailed, Cause: errors.New("provider refused completion")}, outcome: ModelOutcomeFailed},
		{name: "unknown", cause: errors.New("lost response"), outcome: ModelOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			settlement := newModelSettlement(nil, test.cause)
			if settlement.Outcome != test.outcome || settlement.Failure != test.cause.Error() || settlement.Response != nil || settlement.Validate() != nil {
				t.Fatalf("settlement=%+v, want %s", settlement, test.outcome)
			}
		})
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
		{name: "unobserved is not a settlement", settlement: ModelSettlement{Outcome: ModelOutcomeUnobserved}},
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
