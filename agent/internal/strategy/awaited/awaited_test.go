package awaited_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/strategy/awaited"
	"github.com/Tangerg/scope/agent/run"
)

var errProtocol = errors.New("test: invalid protocol")

func TestSettlementReadsOnlyTheAwaitedCompletion(t *testing.T) {
	process, err := run.ParseProcessID("root")
	if err != nil {
		t.Fatal(err)
	}
	effect := run.Turn{Process: process, Step: 1}.EffectID(0)
	other := run.Turn{Process: process, Step: 1}.EffectID(1)
	payload, err := run.EncodePayload(7)
	if err != nil {
		t.Fatal(err)
	}
	succeeded, err := run.NewSettlement(run.SettlementStatusSucceeded, payload)
	if err != nil {
		t.Fatal(err)
	}
	failure, err := run.NewFailure(run.FailureKindExternal, "test.failed", "refused")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := run.NewFailedSettlement(failure, run.Payload{})
	if err != nil {
		t.Fatal(err)
	}

	value, settledFailure, err := awaited.Settlement[int]([]run.Message{run.Completion{Effect: effect, Outcome: succeeded}}, effect, errProtocol)
	if err != nil || settledFailure != nil || value != 7 {
		t.Fatalf("success = %d, %v, %v", value, settledFailure, err)
	}
	_, settledFailure, err = awaited.Settlement[int]([]run.Message{run.Completion{Effect: effect, Outcome: failed}}, effect, errProtocol)
	if err != nil || settledFailure == nil || *settledFailure != failure {
		t.Fatalf("failure = %v, %v", settledFailure, err)
	}
	for name, messages := range map[string][]run.Message{
		"none":         nil,
		"two":          {run.Completion{Effect: effect, Outcome: succeeded}, run.Completion{Effect: effect, Outcome: succeeded}},
		"other effect": {run.Completion{Effect: other, Outcome: succeeded}},
		"signal":       {run.Signal{}},
	} {
		if _, _, err := awaited.Settlement[int](messages, effect, errProtocol); !errors.Is(err, errProtocol) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := awaited.Settlement[string]([]run.Message{run.Completion{Effect: effect, Outcome: succeeded}}, effect, errProtocol); !errors.Is(err, errProtocol) {
		t.Errorf("undecodable payload: %v", err)
	}
}
