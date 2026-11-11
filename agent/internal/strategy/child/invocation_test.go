package child

import (
	jsonv2 "encoding/json/v2"
	"fmt"
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
	other := run.Completion{Effect: run.EffectID{Process: parent, Step: 3, Index: 0}, Outcome: value(run.NewSettlement(run.SettlementStatusSucceeded, value(run.EncodePayload(process))))}
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
		if invalid.valid() {
			t.Fatalf("accepted %+v", invalid)
		}
	}
	invocation := Invocation{Request: &id}
	completion := run.Completion{Effect: id, Outcome: value(run.NewSettlement(run.SettlementStatusSucceeded, value(run.EncodePayload(process))))}
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
		if invalid.valid() {
			t.Fatal("invalid obligation accepted")
		}
	}
	for _, message := range []run.Message{
		run.Completion{Effect: id},
		run.Completion{Effect: id, Outcome: value(run.NewSettlement(run.SettlementStatusSucceeded, value(run.ParsePayload([]byte(`3`)))))},
		run.Completion{Effect: id, Outcome: value(run.NewSettlement(run.SettlementStatusUnknown, value(run.ParsePayload([]byte(`null`)))))},
	} {
		invocation := Invocation{Request: &id}
		if accepted, err := invocation.Accept(message); accepted || err == nil {
			t.Fatal("invalid completion accepted", message)
		}
	}
	invocation := Invocation{Request: &id}
	if accepted, err := invocation.Accept(run.Completion{Effect: id, Outcome: value(run.NewFailedSettlement(failure, run.Payload{}))}); !accepted || err != nil || invocation.Request != nil || invocation.Rejection == nil {
		t.Fatal("admission failure was not transferred", err)
	}
	invocation = Invocation{Process: &process}
	if accepted, err := invocation.Accept(run.Signal{}); accepted || err != nil {
		t.Fatal("external input consumed as child message")
	}
	if accepted, err := invocation.Accept(run.Exit{Child: parent}); accepted || err != nil {
		t.Fatal("unrelated exit consumed")
	}
	if _, _, err := Start(run.Turn{}, 0, run.ChildSpec{}); err == nil {
		t.Fatal("invalid start accepted")
	}
}

// A strategy may add context around a child protocol violation, but the
// Failure the Runtime records stays the one this package declares.
func TestInvalidMessageCarriesOneContractClassification(t *testing.T) {
	id := run.EffectID{Process: value(run.ParseProcessID("root")), Step: 1}
	invocation := Invocation{Request: &id}
	_, err := invocation.Accept(run.Completion{Effect: id, Outcome: value(run.NewSettlement(run.SettlementStatusSucceeded, value(run.ParsePayload([]byte(`{"not":"a spawn result"}`)))))})
	failure, classified := run.StepFailure(fmt.Errorf("strategy context: %w", err))
	if !classified || failure.Kind() != run.FailureKindContract || failure.Code() != "strategy.child.message_invalid" {
		t.Fatalf("StepFailure = %+v, %t", failure, classified)
	}
}

func TestBatchParentAdmitsOnlyOneDeclaredBatch(t *testing.T) {
	parent := value(run.ParseProcessID("root"))
	other := value(run.ParseProcessID("other"))
	keys := []run.ChildKey{value(run.ParseChildKey("a")), value(run.ParseChildKey("b"))}
	expected := func(position int) (run.ChildKey, error) { return keys[position], nil }
	request := func(process run.ProcessID, step uint64, index uint32) Invocation {
		return Invocation{Request: &run.EffectID{Process: process, Step: step, Index: index}}
	}
	started := func(process run.ProcessID, key run.ChildKey) Invocation {
		return Invocation{Process: new(value(process.Child(key)))}
	}
	got, err := Batch{request(parent, 2, 0), started(parent, keys[1])}.Parent(expected)
	if err != nil || got != parent {
		t.Fatalf("Parent = %v, %v", got, err)
	}
	for name, batch := range map[string]Batch{
		"index":  {request(parent, 2, 1), started(parent, keys[1])},
		"step":   {request(parent, 2, 0), request(parent, 3, 1)},
		"key":    {started(parent, keys[1]), started(parent, keys[1])},
		"parent": {request(parent, 2, 0), started(other, keys[1])},
	} {
		if _, err := batch.Parent(expected); err == nil {
			t.Fatalf("%s: batch admitted", name)
		}
	}
}
