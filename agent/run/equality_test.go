package run

import (
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

// Log validation proves an intent matches its committed declaration through
// these comparisons, so each must distinguish every fact the wire form records
// and must never accept an invalid value as evidence.
func TestCommittedFactEqualityFollowsEncodedFacts(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	root := controlValue(ParseProcessID("root"))
	child := controlValue(root.Child(controlValue(ParseChildKey("child"))))
	sibling := controlValue(root.Child(controlValue(ParseChildKey("sibling"))))
	spec := ChildSpec{Key: controlValue(ParseChildKey("child")), ProgramRef: program.ProgramRef(), Input: controlValue(EncodePayload(0))}
	signal := Signal{ID: controlValue(ParseSignalID("input")), Payload: controlValue(EncodePayload(1))}
	call := controlValue(NewDispatcherEffect(controlValue(ParsePayload([]byte(`{"b":[1.0,-0.0],"a":"x"}`))), replayNever))
	childStart := controlValue(NewChildStartEffect(spec))
	send := controlValue(NewChildSignalEffect(child, signal))
	cancel := controlValue(NewChildCancelEffect(child, "parent completed"))

	for name, pair := range map[string][2]Effect{
		"zero":    {{}, {}},
		"invalid": {{}, call},
	} {
		if pair[0].Equal(pair[1]) || pair[1].Equal(pair[0]) {
			t.Errorf("%s effect was accepted as committed evidence", name)
		}
	}
	variants := []Effect{call, childStart, send, cancel}
	for i, left := range variants {
		restored := roundTrip[Effect](t, left)
		if !left.Equal(restored) || !restored.Equal(left) {
			t.Errorf("variant %d changed across its wire form", i)
		}
		for j, right := range variants {
			if i != j && left.Equal(right) {
				t.Errorf("variants %d and %d compared equal", i, j)
			}
		}
	}

	if !call.Equal(controlValue(NewDispatcherEffect(controlValue(ParsePayload([]byte(` { "a" : "x", "b" : [1.0, -0.0] } `))), replayNever))) {
		t.Error("canonically equal payloads compared unequal")
	}
	raw := []byte(`{"n":1}`)
	owned := controlValue(NewDispatcherEffect(controlValue(ParsePayload(raw)), replayNever))
	raw[5] = '2'
	if !owned.Equal(controlValue(NewDispatcherEffect(controlValue(ParsePayload([]byte(`{"n":1}`))), replayNever))) {
		t.Error("caller bytes changed a constructed effect")
	}
	otherProgram := spec.ProgramRef
	otherProgram.revision += "-other"
	otherCapabilities := spec
	otherCapabilities.Capabilities = controlValue(NewCapabilitySet(controlValue(ParseCapability("tests.write"))))
	wait := controlValue(ParseWaitKey("answer"))
	for name, changed := range map[string]Effect{
		"literal 1 and 1.0":   controlValue(NewDispatcherEffect(controlValue(ParsePayload([]byte(`1`))), replayNever)),
		"null payload":        controlValue(NewDispatcherEffect(controlValue(ParsePayload([]byte(`null`))), replayNever)),
		"child start key":     controlValue(NewChildStartEffect(ChildSpec{Key: controlValue(ParseChildKey("other")), ProgramRef: spec.ProgramRef, Input: spec.Input})),
		"child start program": controlValue(NewChildStartEffect(ChildSpec{Key: spec.Key, ProgramRef: otherProgram, Input: spec.Input})),
		"child start input":   controlValue(NewChildStartEffect(ChildSpec{Key: spec.Key, ProgramRef: spec.ProgramRef, Input: controlValue(EncodePayload(1))})),
		"child start budget":  controlValue(NewChildStartEffect(ChildSpec{Key: spec.Key, ProgramRef: spec.ProgramRef, Input: spec.Input, Budget: Budget{Steps: NewQuota(3)}})),
		"child start grants":  controlValue(NewChildStartEffect(otherCapabilities)),
		"signal child":        controlValue(NewChildSignalEffect(sibling, signal)),
		"signal id":           controlValue(NewChildSignalEffect(child, Signal{ID: controlValue(ParseSignalID("other")), Payload: signal.Payload})),
		"signal wait":         controlValue(NewChildSignalEffect(child, Signal{ID: signal.ID, Wait: wait, Payload: signal.Payload})),
		"signal payload":      controlValue(NewChildSignalEffect(child, Signal{ID: signal.ID, Payload: controlValue(EncodePayload(2))})),
		"cancel child":        controlValue(NewChildCancelEffect(sibling, "parent completed")),
		"cancel reason":       controlValue(NewChildCancelEffect(child, "superseded")),
	} {
		for _, original := range variants {
			if original.Equal(changed) || changed.Equal(original) {
				t.Errorf("%s compared equal to an original effect", name)
			}
		}
	}
	if !controlValue(NewDispatcherEffect(controlValue(ParsePayload([]byte(`1`))), replayNever)).Equal(controlValue(NewDispatcherEffect(controlValue(ParsePayload([]byte(`1`))), replayNever))) {
		t.Error("identical literal payloads compared unequal")
	}

	question := kernelWait()
	transition := controlValue(Yield(1, []Effect{call, childStart}, question))
	if restored := roundTrip[Transition](t, transition); !transition.equal(restored) {
		t.Error("transition changed across its wire form")
	}
	unvalidated := question
	unvalidated.AnswerSchema = jsonschema.Schema{}
	widened := question
	widened.MaxAnswerBytes++
	asked := question
	asked.Question = controlValue(EncodePayload("Stop?"))
	stringAnswer := question
	stringAnswer.AnswerSchema = controlValue(jsonschema.For[string]())
	failure := controlValue(NewFailure(FailureKindExecution, "tests.failed", "failed"))
	for name, changed := range map[string]Transition{
		"consumed":      controlValue(Yield(2, []Effect{call, childStart}, question)),
		"effect order":  controlValue(Yield(1, []Effect{childStart, call}, question)),
		"effect count":  controlValue(Yield(1, []Effect{call}, question)),
		"kind":          controlValue(Continue(1, call, childStart)),
		"answer schema": controlValue(Yield(1, []Effect{call, childStart}, unvalidated)),
		"schema shape":  controlValue(Yield(1, []Effect{call, childStart}, stringAnswer)),
		"answer bytes":  controlValue(Yield(1, []Effect{call, childStart}, widened)),
		"question":      controlValue(Yield(1, []Effect{call, childStart}, asked)),
	} {
		if transition.equal(changed) || changed.equal(transition) {
			t.Errorf("%s change compared equal", name)
		}
	}
	output := controlValue(Complete(0, controlValue(EncodePayload(1))))
	if output.equal(controlValue(Complete(0, controlValue(EncodePayload(2))))) || !output.equal(controlValue(Complete(0, controlValue(EncodePayload(1))))) {
		t.Error("completion output was not compared by value")
	}
	failed := controlValue(Fail(0, failure))
	if failed.equal(controlValue(Fail(0, controlValue(NewFailure(FailureKindExecution, "tests.failed", "other"))))) || !failed.equal(controlValue(Fail(0, failure))) {
		t.Error("failure was not compared by value")
	}
	if (Transition{}).equal(Transition{}) {
		t.Error("invalid transition was accepted as committed evidence")
	}
}

func roundTrip[T any](t *testing.T, value T) T {
	t.Helper()
	var restored T
	if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(value)), &restored); err != nil {
		t.Fatal(err)
	}
	return restored
}
