package child

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func value[T any](result T, err error) T {
	if err != nil {
		panic(err)
	}
	return result
}
func TestInvocationRetainsOnlyOutstandingObligation(t *testing.T) {
	parent := value(run.ParseProcessID("root"))
	process := value(parent.Child(value(run.ParseChildKey("worker"))))
	id := run.EffectID{Process: parent, Step: 3, Index: 1}
	invocation := Invocation{Request: &id}
	other := run.Completion{Effect: run.EffectID{Process: parent, Step: 3, Index: 0}, Outcome: value(run.NewSettlement(run.SettlementStatusSucceeded, value(run.EncodePayload(process)).JSON()))}
	if matched, err := invocation.Accept(other); matched || err != nil {
		t.Fatalf("unrelated completion=%t %v", matched, err)
	}
	completion := other
	completion.Effect = id
	if matched, err := invocation.Accept(completion); !matched || err != nil {
		t.Fatal(matched, err)
	}
	if invocation.Request != nil || invocation.Process == nil || *invocation.Process != process || invocation.Done() {
		t.Fatal("spawn receipt retained duplicate lifecycle")
	}
	var termination run.Termination
	if err := jsonv2.Unmarshal([]byte(`{"output":"done"}`), &termination); err != nil {
		t.Fatal(err)
	}
	if matched, err := invocation.Accept(run.Exit{Child: process, Termination: termination}); !matched || err != nil {
		t.Fatal(matched, err)
	}
	if invocation.Process != nil || !invocation.Done() || invocation.Termination == nil {
		t.Fatal("exit retained active membership")
	}
}

func TestInvocationRejectsContradictoryStateAndForeignParent(t *testing.T) {
	parent := value(run.ParseProcessID("root"))
	id := run.EffectID{Process: parent, Step: 1}
	process := value(run.ParseProcessID("other/worker"))
	for _, invalid := range []Invocation{{}, {Request: &id, Process: &process}, {Process: &parent}, {Request: new(run.EffectID)}} {
		if invalid.Valid() {
			t.Fatalf("accepted %+v", invalid)
		}
	}
	invocation := Invocation{Request: &id}
	completion := run.Completion{Effect: id, Outcome: value(run.NewSettlement(run.SettlementStatusSucceeded, value(run.EncodePayload(process)).JSON()))}
	if matched, err := invocation.Accept(completion); matched || err == nil {
		t.Fatal("foreign child was accepted")
	}
	if invocation.Request == nil || invocation.Process != nil {
		t.Fatal("rejected completion advanced state")
	}
}

func TestChildProtocolRejectsMalformedOrOutOfPhaseMessages(t *testing.T) {
	parent := value(run.ParseProcessID("root"))
	process := value(run.ParseProcessID("root/child"))
	id := run.EffectID{Process: parent, Step: 1}
	failure := value(run.NewFailure(run.FailureKindExternal, "test.refused", "refused"))
	for _, invalid := range []Invocation{{Termination: new(run.Termination)}, {Rejection: new(run.Failure)}, {Process: new(run.ProcessID)}} {
		if invalid.Valid() {
			t.Fatal("invalid obligation accepted")
		}
	}
	for _, message := range []run.Message{
		run.Completion{Effect: id},
		run.Completion{Effect: id, Outcome: value(run.NewSettlement(run.SettlementStatusSucceeded, []byte(`3`)))},
		run.Completion{Effect: id, Outcome: value(run.NewSettlement(run.SettlementStatusFailed, []byte(`{}`)))},
		run.Completion{Effect: id, Outcome: value(run.NewSettlement(run.SettlementStatusUnknown, []byte(`null`)))},
	} {
		invocation := Invocation{Request: &id}
		if accepted, err := invocation.Accept(message); accepted || err == nil {
			t.Fatal("invalid completion accepted", message)
		}
	}
	invocation := Invocation{Request: &id}
	if accepted, err := invocation.Accept(run.Completion{Effect: id, Outcome: value(run.NewSettlement(run.SettlementStatusFailed, value(run.EncodePayload(failure)).JSON()))}); !accepted || err != nil || invocation.Request != nil || invocation.Rejection == nil {
		t.Fatal("admission failure was not transferred", err)
	}
	invocation = Invocation{Process: &process}
	if accepted, err := invocation.Accept(run.Exit{Child: process}); accepted || err == nil {
		t.Fatal("invalid terminal fact accepted")
	}
	if accepted, err := invocation.Accept(run.Signal{}); accepted || err != nil {
		t.Fatal("external input consumed as child message")
	}
	if accepted, err := invocation.Accept(run.Exit{Child: parent}); accepted || err != nil {
		t.Fatal("unrelated exit consumed")
	}
	var absent *Invocation
	if accepted, err := absent.Accept(run.Signal{}); accepted || err == nil {
		t.Fatal("nil invocation accepted input")
	}
	if _, _, err := Start(run.Turn{}, 0, run.ChildSpec{}); err == nil {
		t.Fatal("invalid start accepted")
	}
}
