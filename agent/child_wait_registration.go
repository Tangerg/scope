package agent

import (
	"cmp"
	"iter"
	"slices"
)

type childWaitRegistration struct {
	waitID WaitID
	spec   ChildWaitSpec
}

// childWaitRegistry owns the active child-wait registrations of one tree,
// grouped by parent. A parent group exists only while it holds a registration.
type childWaitRegistry map[ProcessID]map[WaitID]*childWaitRegistration

// add reports false when parentID already registered the same WaitID.
func (c childWaitRegistry) add(parentID ProcessID, registration *childWaitRegistration) bool {
	if c[parentID][registration.waitID] != nil {
		return false
	}
	if c[parentID] == nil {
		c[parentID] = make(map[WaitID]*childWaitRegistration)
	}
	c[parentID][registration.waitID] = registration
	return true
}

func (c childWaitRegistry) remove(parentID ProcessID, waitID WaitID) {
	registrations := c[parentID]
	delete(registrations, waitID)
	if len(registrations) == 0 {
		delete(c, parentID)
	}
}

// ordered returns parentID's registrations in WaitID order so notification
// does not depend on map iteration.
func (c childWaitRegistry) ordered(parentID ProcessID) []*childWaitRegistration {
	ordered := make([]*childWaitRegistration, 0, len(c[parentID]))
	for _, registration := range c[parentID] {
		ordered = append(ordered, registration)
	}
	slices.SortFunc(ordered, func(left, right *childWaitRegistration) int {
		return cmp.Compare(left.waitID.String(), right.waitID.String())
	})
	return ordered
}

// wire leaves ordering to treeSnapshotWire.normalize, which owns the
// canonical encoding.
func (c childWaitRegistry) wire() []childWaitSnapshotWire {
	var waits []childWaitSnapshotWire
	for parentID, registrations := range c {
		for _, registration := range registrations {
			waits = append(waits, childWaitSnapshotWire{
				ParentProcessID: parentID, WaitID: registration.waitID, Spec: registration.spec.wire(),
			})
		}
	}
	return waits
}

// register validates spec against the tree's relations, records it, and
// returns the answer when the tree already satisfies it. A registration whose
// answer cannot be encoded is withdrawn.
func (c childWaitRegistry) register(parentID ProcessID, waitID WaitID, spec ChildWaitSpec, members *treeMembers) (Signal, bool, error) {
	if !parentID.Valid() || !waitID.Valid() || !spec.Valid() || members.get(parentID) == nil {
		return Signal{}, false, ErrInvalidChildWait
	}
	if err := spec.validateRelations(parentID, members.relation); err != nil {
		return Signal{}, false, err
	}
	registration := &childWaitRegistration{waitID: waitID, spec: spec.clone()}
	if !c.add(parentID, registration) {
		return Signal{}, false, ErrInvalidChildWait
	}
	signal, satisfied, err := registration.satisfaction(members)
	if err != nil {
		c.remove(parentID, waitID)
	}
	return signal, satisfied, err
}

// awaiting yields parentID's registrations that wait for childID at boundary,
// in WaitID order.
func (c childWaitRegistry) awaiting(parentID, childID ProcessID, boundary ChildWaitBoundary) iter.Seq[*childWaitRegistration] {
	return func(yield func(*childWaitRegistration) bool) {
		for _, registration := range c.ordered(parentID) {
			if registration.spec.Boundary != boundary || !slices.Contains(registration.spec.Children, childID) {
				continue
			}
			if !yield(registration) {
				return
			}
		}
	}
}

// satisfaction returns the answer Signal once enough watched children have
// reached the registration's boundary. Registration proves membership, and
// children stay retained until tree release.
func (c *childWaitRegistration) satisfaction(members *treeMembers) (Signal, bool, error) {
	spec := c.spec
	outcomes := make([]ChildOutcome, 0, len(spec.Children))
	for _, childID := range spec.Children {
		child := members.get(childID)
		ready := child.status.Terminal()
		if spec.Boundary == ChildWaitBoundaryDrained {
			ready = child.handle.joinDone() && child.handle.joinError() == nil
		}
		if !ready {
			continue
		}
		key, _ := child.handle.relation.ChildKey()
		outcome := ChildOutcome{key: key, result: child.result(), boundary: spec.Boundary}
		if spec.Boundary == ChildWaitBoundaryDrained {
			outcome.subtreeUnresolvedEffects = members.subtreeUnresolvedEffects(childID)
		}
		outcomes = append(outcomes, outcome)
	}
	if uint32(len(outcomes)) < spec.required() {
		return Signal{}, false, nil
	}
	signal, err := encodeChildWaitSatisfied(c.waitID, spec.Key, spec.Boundary, outcomes)
	if err != nil {
		return Signal{}, false, err
	}
	return signal, true, nil
}
