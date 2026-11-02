package run

import (
	"errors"
	"testing"
)

func TestProcessRegistrationRequiresTheExactReservedRelation(t *testing.T) {
	tree, parent := newChildCompletionTestProcess(t)
	engine := tree.engine
	if err := engine.reserveProcessStart(parent.handle.relation); err != nil {
		t.Fatal(err)
	}
	engine.publishProcessStart(parent.handle)

	first := childProcessRelation(newProcessID(), parent.handle.relation, controlValue(ParseChildKey("first")))
	foreign := first
	foreign.rootID = newProcessID()
	if err := engine.reserveProcessStart(foreign); !errors.Is(err, ErrInvalidProcessRelation) {
		t.Fatalf("reserved a child in a different tree from its parent: %v", err)
	}
	second := childProcessRelation(newProcessID(), parent.handle.relation, controlValue(ParseChildKey("second")))
	for _, relation := range []ProcessRelation{first, second} {
		if err := engine.reserveProcessStart(relation); err != nil {
			t.Fatal(err)
		}
		if _, exists := engine.Process(relation.ProcessID()); exists {
			t.Fatal("unpublished reservation returned a handle")
		}
	}
	newHandle := func(relation ProcessRelation) *processHandle {
		h := newProcessHandle(relation, parent.deployment, parent.handle.budget, parent.handle.capabilities, parent.handle.startedAt)
		h.tree = tree.binding
		return h
	}
	wrong := childProcessRelation(first.ProcessID(), parent.handle.relation, second.childKey)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("published one candidate under the other candidate's child key")
			}
		}()
		engine.publishProcessStart(newHandle(wrong))
	}()
	engine.discardProcessStart(wrong)
	for _, relation := range []ProcessRelation{first, second} {
		engine.publishProcessStart(newHandle(relation))
		got, found := engine.Process(relation.ProcessID())
		if !found || got.Relation() != relation {
			t.Fatalf("reservation lost: %v", relation)
		}
		got.handle.publishResult(Result[Payload]{})
		got.handle.finishBookkeeping()
	}
	parent.handle.publishResult(Result[Payload]{})
	parent.handle.finishBookkeeping()
}
