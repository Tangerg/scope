package agent

import (
	"cmp"
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
// and encoding do not depend on map iteration.
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

func (c childWaitRegistry) wire() []childWaitSnapshotWire {
	var waits []childWaitSnapshotWire
	for parentID := range c {
		for _, registration := range c.ordered(parentID) {
			waits = append(waits, childWaitSnapshotWire{
				ParentProcessID: parentID, WaitID: registration.waitID, Spec: registration.spec.wire(),
			})
		}
	}
	return waits
}
