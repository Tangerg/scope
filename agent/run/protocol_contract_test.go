package run

import (
	"encoding"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestPathIdentityOwnsAncestryAndCoordinates(t *testing.T) {
	root := (StartID{Scope: "tests", Key: "path"}).Root()
	child := controlValue(root.Child(controlValue(ParseChildKey("worker"))))
	grandchild := controlValue(child.Child(controlValue(ParseChildKey("task"))))
	if grandchild.Root() != root || grandchild.Depth() != 2 || !root.Contains(grandchild) || !child.Contains(grandchild) || grandchild.Contains(root) {
		t.Fatal("ancestry was not derived from the path")
	}
	if parent, ok := grandchild.Parent(); !ok || parent != child {
		t.Fatalf("parent=%s present=%t", parent, ok)
	}
	if key, ok := grandchild.Key(); !ok || key.String() != "task" {
		t.Fatalf("key=%s present=%t", key, ok)
	}
	if _, ok := root.Key(); ok || root.Contains(controlValue(ParseProcessID(root.String()+"suffix"))) {
		t.Fatal("root path was matched by a textual prefix")
	}
	for _, value := range []string{"", "/root", "root/", "root//task", "root/../task", "root/a b", strings.Repeat("x", maxIdentityBytes+1)} {
		if _, err := ParseProcessID(value); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("accepted invalid path %q: %v", value, err)
		}
	}
	for _, value := range []EffectID{{Process: grandchild, Step: 1}, {Process: root, Step: math.MaxUint64, Index: math.MaxUint32}} {
		parsed := controlValue(ParseEffectID(value.String()))
		if parsed != value {
			t.Fatal("structural EffectID lost coordinates")
		}
		wire := controlValue(jsonv2.Marshal(value))
		var decoded EffectID
		if err := jsonv2.Unmarshal(wire, &decoded); err != nil || decoded != value {
			t.Fatalf("effect wire=%s error=%v", wire, err)
		}
		if err := controlValue(jsonschema.For[EffectID]()).Validate(wire); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"root/0/0", "root/01/0", "root/1/01", "root/-1/0", "root/1/4294967296", "root/a/0", "root/1", "root/18446744073709551616/0"} {
		if _, err := ParseEffectID(value); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("accepted invalid coordinates %q", value)
		}
	}
	for _, wire := range []string{`null`, `{}`, `{"process":"root","step":1}`, `{"process":"root","step":1,"index":0,"attempt":1}`} {
		var id EffectID
		if err := jsonv2.Unmarshal([]byte(wire), &id); err == nil {
			t.Fatalf("accepted ambiguous EffectID %s", wire)
		}
	}
}

func TestLogicalKeysHaveOneStrictTextRepresentation(t *testing.T) {
	for _, value := range []string{"logical:request-1", "alpha.beta", "a_b"} {
		for _, destination := range []encoding.TextUnmarshaler{new(SignalID), new(WaitKey), new(ChildKey)} {
			if err := destination.UnmarshalText([]byte(value)); err != nil {
				t.Fatal(err)
			}
			encoded, err := destination.(encoding.TextMarshaler).MarshalText()
			if err != nil || string(encoded) != value {
				t.Fatalf("key was silently normalized: %s %v", encoded, err)
			}
		}
	}
	for _, value := range []string{"", ".", "..", "x/y", "x%20y", " a", "a\n", "中文"} {
		for _, destination := range []encoding.TextUnmarshaler{new(SignalID), new(WaitKey), new(ChildKey)} {
			if err := destination.UnmarshalText([]byte(value)); !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("invalid key %q admitted by %T: %v", value, destination, err)
			}
		}
	}
}

func TestInboxPersistsClosedMessageVariants(t *testing.T) {
	root := controlValue(ParseProcessID("root"))
	child := controlValue(root.Child(controlValue(ParseChildKey("child"))))
	outcome := controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`null`)))
	inbox := Inbox{
		Signal{ID: controlValue(ParseSignalID("application:ordinary-user-id")), Payload: controlValue(EncodePayload("wait_opened"))},
		Completion{Effect: EffectID{Process: root, Step: 1}, Outcome: outcome},
		Exit{Child: child, Termination: Termination{output: controlValue(EncodePayload(0))}},
	}
	wire := controlValue(jsonv2.Marshal(inbox))
	var restored Inbox
	if err := jsonv2.Unmarshal(wire, &restored); err != nil {
		t.Fatal(err)
	}
	if !(Turn{Process: root, Step: 2, Messages: restored}).Valid() {
		t.Fatal("valid typed messages did not survive persistence")
	}
	if _, ok := restored[0].(Signal); !ok {
		t.Fatal("a user signal's identifier or payload changed its kind")
	}
	for _, bad := range []Inbox{{nil}, {new(Signal)}, {Signal{}}, {Completion{Effect: EffectID{Process: root, Step: 1}, Outcome: unknownSettlement()}}, {Exit{}}} {
		if _, err := jsonv2.Marshal(bad); err == nil || (Turn{Process: root, Step: 1, Messages: bad}).Valid() {
			t.Fatalf("invalid message admitted: %#v", bad)
		}
	}
	for _, raw := range []string{`[{}]`, `[null]`, `[{"signal":null}]`, `[{"completion":{},"signal":{}}]`, `[{"opened":{}}]`} {
		if err := jsonv2.Unmarshal([]byte(raw), &restored); err == nil {
			t.Fatalf("unknown or mixed message accepted: %s", raw)
		}
	}
}

func TestEffectsAndTransitionsUseOneCurrentWireSchema(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	root := controlValue(ParseProcessID("root"))
	child := controlValue(root.Child(controlValue(ParseChildKey("child"))))
	signal := Signal{ID: controlValue(ParseSignalID("input")), Payload: controlValue(EncodePayload(1))}
	effects := []Effect{
		controlValue(NewDispatcherEffect([]byte(`{}`))),
		controlValue(NewChildStartEffect(ChildSpec{Key: controlValue(ParseChildKey("child")), ProgramRef: program.ProgramRef(), Input: controlValue(EncodePayload(0))})),
		controlValue(NewChildSignalEffect(child, signal)),
		controlValue(NewChildCancelEffect(child, "parent completed")),
	}
	for _, effect := range effects {
		wire := controlValue(jsonv2.Marshal(effect))
		var restored Effect
		if err := jsonv2.Unmarshal(wire, &restored); err != nil || !restored.equal(effect) {
			t.Fatalf("effect wire %s: %v", wire, err)
		}
		transition := controlValue(Yield(0, []Effect{effect}, kernelWait()))
		wire = controlValue(jsonv2.Marshal(transition))
		var decoded Transition
		if err := jsonv2.Unmarshal(wire, &decoded); err != nil || !decoded.equal(transition) {
			t.Fatalf("transition wire %s: %v", wire, err)
		}
	}
	if spec, ok := effects[1].ChildStart(); !ok || spec.ProgramRef != program.ProgramRef() || spec.Key.String() != "child" {
		t.Fatalf("spawn declaration changed: %+v", spec)
	}
	if _, ok := effects[0].ChildStart(); ok {
		t.Fatal("Call was decoded as Spawn")
	}
	for _, raw := range []string{`{}`, `{"call":0,"spawn":{}}`, `{"operation":"wait","payload":{}}`, `{"send":{"child":"root","signal":{}}}`, `{"cancel":{"child":"root","reason":" "}}`} {
		var effect Effect
		if err := jsonv2.Unmarshal([]byte(raw), &effect); err == nil {
			t.Fatalf("invalid Effect accepted: %s", raw)
		}
	}
	for _, raw := range []string{`{}`, `{"kind":"continue"}`, `{"kind":"continue","consumed":0,"output":null}`, `{"kind":"checkpoint","consumed":0}`, `{"kind":"yield","consumed":0,"waits":[{}]}`, `{"kind":"fail","consumed":0}`} {
		var transition Transition
		if err := jsonv2.Unmarshal([]byte(raw), &transition); err == nil {
			t.Fatalf("invalid Transition accepted: %s", raw)
		}
	}
	if !controlValue(Continue(0)).equal(controlValue(Continue(0, []Effect{}...))) {
		t.Fatal("empty and absent effect lists changed the same transition")
	}
	var policy EffectPolicy
	if err := jsonv2.Unmarshal([]byte(`{"replay":"never"}`), &policy); err == nil {
		t.Fatal("a persisted policy omitted its capability declaration")
	}
	for _, raw := range []string{`{"replay":"never","required_capabilities":[]}`, `{"replay":"same_identity","required_capabilities":[]}`} {
		if err := jsonv2.Unmarshal([]byte(raw), &policy); err != nil || !policy.Valid() {
			t.Fatalf("valid policy rejected: %v", err)
		}
	}
}

func TestTransitionConstructionRejectsIncompleteDecisions(t *testing.T) {
	for _, test := range []struct {
		name string
		make func() (Transition, error)
	}{
		{"invalid effect", func() (Transition, error) { return Continue(0, Effect{}) }},
		{"invalid wait", func() (Transition, error) { return Yield(0, nil, Wait{}) }},
		{"repeated wait", func() (Transition, error) { return Yield(0, nil, kernelWait(), kernelWait()) }},
		{"absent output", func() (Transition, error) { return Complete(0, Payload{}) }},
		{"absent failure", func() (Transition, error) { return Fail(0, Failure{}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			transition, err := test.make()
			if !errors.Is(err, ErrInvalidTransition) || transition.Valid() {
				t.Fatalf("incomplete decision was published: transition=%+v error=%v", transition, err)
			}
		})
	}
	for _, kind := range []TransitionKind{TransitionKindContinue, TransitionKindYield, TransitionKindComplete, TransitionKindFail} {
		if !kind.Valid() || kind.String() != string(kind) {
			t.Fatalf("valid transition vocabulary changed: %q", kind)
		}
	}
	if TransitionKindInvalid.Valid() || TransitionKind("retired").String() != invalidEnumName {
		t.Fatal("unknown transition became a public state")
	}
	failure := controlValue(NewFailure(FailureKindExecution, "test.failed", "step rejected"))
	decision := controlValue(Fail(2, failure))
	var restored Transition
	if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(decision)), &restored); err != nil {
		t.Fatal(err)
	}
	if actual, ok := restored.Failure(); !ok || actual != failure || restored.Kind() != TransitionKindFail || restored.Consumed() != 2 {
		t.Fatal("failure transition lost its domain decision")
	}
	if _, err := jsonv2.Marshal(Transition{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("zero transition encoded as a decision: %v", err)
	}
}

func TestCapabilityGrantsRejectMalformedAuthority(t *testing.T) {
	for _, name := range []string{"", "Read.files", " files.read", "files/read", "files read"} {
		if _, err := ParseCapability(name); !errors.Is(err, ErrInvalidCapability) {
			t.Fatalf("invalid authority %q admitted: %v", name, err)
		}
		var capability Capability
		if err := capability.UnmarshalText([]byte(name)); !errors.Is(err, ErrInvalidCapability) || capability.Valid() {
			t.Fatalf("invalid wire authority %q admitted: %v", name, err)
		}
	}
	if _, err := NewCapabilitySet(Capability{}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatal("a zero capability became a grant")
	}
	read := controlValue(ParseCapability("files.read"))
	write := controlValue(ParseCapability("files.write"))
	grants := controlValue(NewCapabilitySet(write, read, read))
	if read.String() != "files.read" || grants.Contains(Capability{}) || !grants.Contains(read) || !grants.Contains(write) {
		t.Fatal("grant membership changed authority semantics")
	}
	values := grants.Values()
	values[0] = Capability{}
	if !grants.Contains(read) || string(controlValue(jsonv2.Marshal(grants))) != `["files.read","files.write"]` {
		t.Fatal("a grant view changed the compiled authority")
	}
	for _, wire := range []string{`null`, `{}`, `[null]`, `["invalid/name"]`, `["files.read","files.read"]`} {
		var restored CapabilitySet
		if err := jsonv2.Unmarshal([]byte(wire), &restored); err == nil {
			t.Fatalf("invalid persisted authority accepted: %s", wire)
		}
	}
}
