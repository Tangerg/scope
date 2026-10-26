package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"
)

func TestChildWaitRequiresDistinctValidChildrenAndAchievableQuorum(t *testing.T) {
	first, second := newProcessID(), newProcessID()
	base := ChildWaitSpec{
		Key: controlValue(ParseWaitKey("completion")), Children: []ProcessID{first, second},
		Boundary: ChildWaitBoundaryDrained, Condition: AllChildren(),
	}
	for _, test := range []struct {
		name   string
		mutate func(*ChildWaitSpec)
	}{
		{"missing condition", func(spec *ChildWaitSpec) { spec.Condition = ChildWaitCondition{} }},
		{"no children", func(spec *ChildWaitSpec) { spec.Children = nil }},
		{"invalid child", func(spec *ChildWaitSpec) { spec.Children[0] = ProcessID{} }},
		{"duplicate child", func(spec *ChildWaitSpec) { spec.Children[1] = spec.Children[0] }},
		{"unachievable quorum", func(spec *ChildWaitSpec) { spec.Condition = controlValue(ChildQuorum(3)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base.clone()
			test.mutate(&candidate)
			if effect, err := NewChildWaitEffect(candidate); effect.Valid() || !errors.Is(err, ErrInvalidChildWait) {
				t.Fatalf("invalid child set = %v, %v", effect, err)
			}
		})
	}
	if quorum, err := ChildQuorum(0); quorum.Valid() || !errors.Is(err, ErrInvalidChildWait) {
		t.Fatalf("zero quorum = %v, %v", quorum, err)
	}
	for _, condition := range []ChildWaitCondition{AllChildren(), AnyChild(), controlValue(ChildQuorum(2))} {
		spec := base.clone()
		spec.Condition = condition
		effect := controlValue(NewChildWaitEffect(spec))
		spec.Children[0] = newProcessID()
		decoded := controlValue(decodeChildWaitEffect(effect.Payload()))
		if decoded.Children[0] != first || decoded.Children[1] != second {
			t.Fatal("caller mutation changed the managed child wait")
		}
	}
}

func TestChildWaitResponsesRejectIncompleteAndContradictoryOutcomes(t *testing.T) {
	result, present := completedEngineTestSnapshot(t).Result()
	if !present {
		t.Fatal("fixture has no completed result")
	}
	waitID := newProcessID().effectID(1, 0).waitID()
	base := childWaitSatisfiedWire{
		Operation: childWaitSignalSatisfied,
		Outcomes:  []childOutcomeWire{{Result: result.wire(), DescendantUnresolvedEffects: new([]UnresolvedEffect{})}},
	}
	valid := controlValue(NewSignal(waitID.childWaitSignalID(), waitID, controlValue(jsonv2.Marshal(base))))
	if satisfied := controlValue(ParseChildWaitSatisfied(valid)); !satisfied.Valid() || !satisfied.Outcomes()[0].SubtreeResolved() {
		t.Fatal("complete drained evidence was rejected")
	}
	for _, test := range []struct {
		name   string
		mutate func(*childWaitSatisfiedWire)
	}{
		{"no outcomes", func(wire *childWaitSatisfiedWire) { wire.Outcomes = nil }},
		{"wrong operation", func(wire *childWaitSatisfiedWire) { wire.Operation = childWaitSignalOpened }},
		{"unfinished result", func(wire *childWaitSatisfiedWire) { wire.Outcomes[0].Result.FinishedAt = time.Time{} }},
		{"duplicate child result", func(wire *childWaitSatisfiedWire) { wire.Outcomes = append(wire.Outcomes, wire.Outcomes[0]) }},
		{"child repeats its own unresolved evidence", func(wire *childWaitSatisfiedWire) {
			wire.Outcomes[0].DescendantUnresolvedEffects = new([]UnresolvedEffect{{ProcessID: result.ProcessID(), EffectID: result.ProcessID().effectID(2, 0)}})
		}},
		{"duplicate descendant evidence", func(wire *childWaitSatisfiedWire) {
			child := newProcessID()
			effect := UnresolvedEffect{ProcessID: child, EffectID: child.effectID(2, 0)}
			wire.Outcomes[0].DescendantUnresolvedEffects = new([]UnresolvedEffect{effect, effect})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := base
			wire.Outcomes = append([]childOutcomeWire(nil), base.Outcomes...)
			test.mutate(&wire)
			signal := controlValue(NewSignal(waitID.childWaitSignalID(), waitID, controlValue(jsonv2.Marshal(wire))))
			if satisfaction, err := ParseChildWaitSatisfied(signal); satisfaction.Valid() || !errors.Is(err, ErrInvalidChildWait) {
				t.Fatalf("invalid completion = %v, %v", satisfaction, err)
			}
			if len(wire.Outcomes) == 1 && wire.Operation == childWaitSignalSatisfied {
				accepted := ChildOutcome{result: result}
				before := string(controlValue(jsonv2.Marshal(accepted)))
				if err := accepted.UnmarshalJSON(controlValue(jsonv2.Marshal(wire.Outcomes[0]))); !errors.Is(err, ErrInvalidChildWait) || string(controlValue(jsonv2.Marshal(accepted))) != before {
					t.Fatalf("invalid outcome replaced previous evidence: %v", err)
				}
			}
		})
	}
	for _, payload := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"operation":"other"}`)} {
		signal := controlValue(NewSignal(waitID.childWaitSignalID(), waitID, payload))
		if _, err := ParseChildWaitSatisfied(signal); !errors.Is(err, ErrInvalidChildWait) {
			t.Fatal(err)
		}
		if _, err := ParseChildWaitOpened(signal); !errors.Is(err, ErrInvalidChildWait) {
			t.Fatal(err)
		}
	}
}
