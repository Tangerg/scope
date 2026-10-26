package agent

import (
	"bytes"
	"errors"
	"testing"
	"time"

	jsonv2 "encoding/json/v2"
)

func TestEventRejectsMissingIdentityAndMixedScopesBeforePublication(t *testing.T) {
	processID := controlValue(ParseProcessID("process:observation-contract"))
	deployment := newChildTestDeployment(t)
	for _, test := range []struct {
		name   string
		change func(*eventDraft)
	}{
		{"deployment", func(f *eventDraft) { f.deploymentRef = DeploymentRef{} }},
		{"relation", func(f *eventDraft) { f.relation = ProcessRelation{} }},
		{"name", func(f *eventDraft) { f.name = "Invalid Name" }},
		{"time", func(f *eventDraft) { f.occurredAt = time.Time{} }},
		{"payload", func(f *eventDraft) { f.payload = []byte(`{`) }},
		{"process with step", func(f *eventDraft) { f.stepSequence = 1 }},
		{"process with effect", func(f *eventDraft) { f.effectID = processID.effectID(1, 0) }},
		{"step without sequence", func(f *eventDraft) { f.name = EventStepStarted }},
		{"step with effect", func(f *eventDraft) {
			f.name = EventStepStarted
			f.stepSequence = 1
			f.effectID = processID.effectID(1, 0)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fact := eventDraft{deploymentRef: deployment.DeploymentRef(), relation: rootProcessRelation(processID), name: EventProcessStarted, occurredAt: time.Now(), payload: []byte(`{}`)}
			test.change(&fact)
			if event, err := newEvent(fact, 1); !errors.Is(err, ErrInvalidEvent) || event.Valid() {
				t.Fatalf("invalid publication accepted: %+v, %v", event, err)
			}
		})
	}
	fact := eventDraft{deploymentRef: deployment.DeploymentRef(), relation: rootProcessRelation(processID), name: EventProcessStarted, occurredAt: time.Now(), payload: []byte(`{}`)}
	if _, err := newEvent(fact, 0); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("missing publication sequence: %v", err)
	}
	event := controlValue(newEvent(fact, 1))
	before := controlValue(jsonv2.Marshal(event))
	for _, data := range [][]byte{[]byte(`{`), []byte(`{"process_sequence":0}`)} {
		if err := event.UnmarshalJSON(data); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("invalid wire accepted: %s, %v", data, err)
		}
		if after := controlValue(jsonv2.Marshal(event)); !bytes.Equal(before, after) {
			t.Fatal("rejected wire replaced the accepted event")
		}
	}
}

func TestChildEventRoundTripRetainsItsStandaloneTreeRelation(t *testing.T) {
	root := rootProcessRelation(controlValue(ParseProcessID("process:event-root")))
	parent := childProcessRelation(controlValue(ParseProcessID("process:event-parent")), root, controlValue(ParseChildKey("parent")))
	relation := childProcessRelation(controlValue(ParseProcessID("process:event-child")), parent, controlValue(ParseChildKey("child")))
	event := controlValue(newEvent(eventDraft{
		deploymentRef: newChildTestDeployment(t).DeploymentRef(), relation: relation,
		name: EventProcessStarted, occurredAt: time.Now(), payload: []byte(`{}`),
	}, 1))
	encoded := controlValue(jsonv2.Marshal(event))
	var restored Event
	if err := jsonv2.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Relation() != relation || !bytes.Equal(encoded, controlValue(jsonv2.Marshal(restored))) {
		t.Fatal("standalone child event lost its parent, root, key, or depth")
	}

	for _, test := range []struct {
		name   string
		change func(*processRelationWire)
	}{
		{"missing parent", func(wire *processRelationWire) { wire.ParentID = nil }},
		{"missing root", func(wire *processRelationWire) { wire.RootID = nil }},
		{"missing key", func(wire *processRelationWire) { wire.ChildKey = nil }},
		{"child at root depth", func(wire *processRelationWire) { wire.Depth = 0 }},
		{"self parent", func(wire *processRelationWire) { wire.ParentID = new(relation.ProcessID()) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var wire eventWire
			if err := jsonv2.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			test.change(&wire.Relation)
			if err := restored.UnmarshalJSON(controlValue(jsonv2.Marshal(wire))); !errors.Is(err, ErrInvalidEvent) || !errors.Is(err, ErrInvalidProcessRelation) {
				t.Fatalf("contradictory child relation accepted or misclassified: %v", err)
			}
			if !bytes.Equal(encoded, controlValue(jsonv2.Marshal(restored))) {
				t.Fatal("rejected relation replaced the accepted child event")
			}
		})
	}
}
