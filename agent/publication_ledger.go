package agent

import "slices"

// publicationLedger owns the committed facts staged until the next
// acknowledged tree cut. Each Process owes at most one terminal publication.
type publicationLedger map[ProcessID]pendingProcessPublication

type pendingProcessPublication []eventDraft

func (p pendingProcessPublication) hasTerminal() bool {
	return slices.ContainsFunc(p, func(event eventDraft) bool { return event.name == EventProcessFinished })
}

func (p publicationLedger) stage(event eventDraft) {
	p[event.processID()] = append(p[event.processID()], event)
}

func (p publicationLedger) owesTerminal(processID ProcessID) bool {
	return p[processID].hasTerminal()
}

// take removes and returns what processID is owed, if anything.
func (p publicationLedger) take(processID ProcessID) (pendingProcessPublication, bool) {
	publication, pending := p[processID]
	delete(p, processID)
	return publication, pending
}
