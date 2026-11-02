package run

// Runtime.mu protects a process identity from reservation through publication.
// A parent-scoped child key stays occupied throughout that transition. The
// tree owner still controls execution and membership.
type processRegistry struct {
	entries  map[ProcessID]processRegistration
	children map[childIdentity]struct{}
}

type processRegistration struct {
	pending *processReservation
	handle  *processHandle
}

type processReservation struct {
	relation    ProcessRelation
	restoration *treeRestoration
}

func newProcessRegistry() processRegistry {
	return processRegistry{
		entries:  make(map[ProcessID]processRegistration),
		children: make(map[childIdentity]struct{}),
	}
}

func (p *processRegistry) reserve(relation ProcessRelation, restoration *treeRestoration) error {
	id := relation.ProcessID()
	if _, exists := p.entries[id]; exists {
		return ErrProcessAlreadyExists
	}
	if identity, child := relation.childIdentity(); child {
		if _, taken := p.children[identity]; taken {
			return ErrInvalidChildStart
		}
		if restoration == nil {
			parent := p.entries[identity.parent].handle
			if parent == nil || childProcessRelation(id, parent.relation, identity.key) != relation {
				return ErrInvalidProcessRelation
			}
		}
		p.children[identity] = struct{}{}
	}
	p.entries[id] = processRegistration{pending: &processReservation{relation: relation, restoration: restoration}}
	return nil
}

func (p *processRegistry) reserved(relation ProcessRelation, restoration *treeRestoration) bool {
	pending := p.entries[relation.ProcessID()].pending
	return pending != nil && pending.relation == relation && pending.restoration == restoration
}

func (p *processRegistry) discard(relation ProcessRelation, restoration *treeRestoration) {
	if p.reserved(relation, restoration) {
		p.remove(relation)
	}
}

func (p *processRegistry) publish(handle *processHandle, restoration *treeRestoration) {
	if !p.reserved(handle.relation, restoration) {
		panic("run: Process publication does not match its reservation")
	}
	if parent, child := handle.relation.ParentID(); child && restoration == nil && p.entries[parent].handle == nil {
		panic("run: child Process has no registered parent")
	}
	p.entries[handle.processID()] = processRegistration{handle: handle}
}

func (p *processRegistry) release(handle *processHandle) {
	if p.entries[handle.processID()].handle == handle {
		p.remove(handle.relation)
	}
}

func (p *processRegistry) remove(relation ProcessRelation) {
	delete(p.entries, relation.ProcessID())
	if identity, child := relation.childIdentity(); child {
		delete(p.children, identity)
	}
}
