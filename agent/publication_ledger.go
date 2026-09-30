package agent

// publicationLedger owns the committed facts staged until the next
// acknowledged tree cut. Each Process owes at most one terminal publication.
type publicationLedger map[ProcessID]pendingProcessPublication

type pendingProcessPublication struct {
	events   []eventFact
	terminal bool
}

func (p publicationLedger) stage(event eventFact) {
	publication := p[event.processID]
	publication.events = append(publication.events, event)
	p[event.processID] = publication
}

func (p publicationLedger) owesTerminal(processID ProcessID) bool {
	return p[processID].terminal
}

func (p publicationLedger) stageTerminal(event eventFact) {
	p.stage(event)
	publication := p[event.processID]
	publication.terminal = true
	p[event.processID] = publication
}

// take removes and returns what processID is owed, if anything.
func (p publicationLedger) take(processID ProcessID) (pendingProcessPublication, bool) {
	publication, pending := p[processID]
	delete(p, processID)
	return publication, pending
}
