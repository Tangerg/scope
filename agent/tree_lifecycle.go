package agent

import (
	"cmp"
	"slices"
)

type childWaitRegistration struct {
	waitID WaitID
	spec   ChildWaitSpec
}

func orderedChildWaitRegistrations(
	registrations map[WaitID]*childWaitRegistration,
) []*childWaitRegistration {
	ordered := make([]*childWaitRegistration, 0, len(registrations))
	for _, registration := range registrations {
		ordered = append(ordered, registration)
	}
	slices.SortFunc(ordered, func(left, right *childWaitRegistration) int {
		return cmp.Compare(left.waitID.String(), right.waitID.String())
	})
	return ordered
}

func containsProcessID(processes []ProcessID, processID ProcessID) bool {
	for _, candidate := range processes {
		if candidate == processID {
			return true
		}
	}
	return false
}
