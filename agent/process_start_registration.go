package agent

type processStartReservation struct {
	relation           ProcessRelation
	deploymentRef      DeploymentRef
	treeLimits         TreeLimits
	childRequestDigest Digest
}

func (e *Engine) reserveProcessStart(
	relation ProcessRelation,
	deploymentRef DeploymentRef,
	treeLimits TreeLimits,
	childRequestDigest Digest,
) error {
	if !relation.Valid() || !deploymentRef.Valid() || !treeLimits.Valid() {
		return ErrInvalidProcessRelation
	}
	reservation := processStartReservation{
		relation:           relation,
		deploymentRef:      deploymentRef,
		treeLimits:         treeLimits,
		childRequestDigest: childRequestDigest,
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closeDone != nil {
		return ErrEngineClosed
	}
	processID := relation.ProcessID()
	if _, exists := e.processes[processID]; exists {
		return ErrProcessAlreadyExists
	}
	if _, exists := e.startReservations[processID]; exists {
		return ErrProcessAlreadyExists
	}
	if e.restoredProcessReserved(processID) {
		return ErrProcessAlreadyExists
	}
	if relation.IsRoot() {
		return e.reserveRootStart(reservation)
	}
	return e.reserveChildStart(reservation)
}

// Hold e.mu so root publication cannot overtake its admission reservation.
func (e *Engine) reserveRootStart(reservation processStartReservation) error {
	if reservation.childRequestDigest.Valid() {
		return ErrInvalidProcessRelation
	}
	e.startReservations[reservation.relation.ProcessID()] = reservation
	return nil
}

// Hold e.mu so child identity and all tree limits are reserved atomically.
func (e *Engine) reserveChildStart(reservation processStartReservation) error {
	relation := reservation.relation
	treeLimits := reservation.treeLimits
	if !reservation.childRequestDigest.Valid() {
		return ErrInvalidProcessRelation
	}
	parentID, child := relation.ParentID()
	key, keyed := relation.ChildKey()
	if !child || !keyed {
		return ErrInvalidProcessRelation
	}
	identity := childIdentity{parent: parentID, key: key}
	if _, exists := e.children[identity]; exists {
		return ErrInvalidChildStart
	}
	if _, exists := e.childStartReservations[identity]; exists {
		return ErrInvalidChildStart
	}
	if e.restoredChildReserved(identity) {
		return ErrInvalidChildStart
	}
	parent := e.processes[parentID]
	if parent == nil {
		return ErrInvalidProcessRelation
	}
	if treeLimits != parent.treeLimits || relation.depth > treeLimits.MaxDepth {
		return ErrResourceLimitExceeded
	}
	childCount, activeChildCount, treeProcessCount := e.reservedTreeCounts(relation.rootID, parentID)
	if childCount >= treeLimits.MaxChildren ||
		activeChildCount >= treeLimits.MaxActiveChildren ||
		treeProcessCount >= treeLimits.MaxTreeProcesses {
		return ErrResourceLimitExceeded
	}
	processID := relation.ProcessID()
	e.startReservations[processID] = reservation
	e.childStartReservations[identity] = processID
	return nil
}

// Hold e.mu so published Processes and pending reservations contribute to one
// consistent limit check.
func (e *Engine) reservedTreeCounts(rootID, parentID ProcessID) (
	childCount uint32,
	activeChildCount uint32,
	treeProcessCount uint32,
) {
	for _, existing := range e.processes {
		if existing.relation.rootID == rootID {
			treeProcessCount++
		}
		if existing.relation.parentID == parentID {
			childCount++
			if !existing.status().Terminal() {
				activeChildCount++
			}
		}
	}
	for _, pending := range e.startReservations {
		if pending.relation.rootID == rootID {
			treeProcessCount++
		}
		if pending.relation.parentID == parentID {
			childCount++
			activeChildCount++
		}
	}
	return childCount, activeChildCount, treeProcessCount
}

func (e *Engine) discardProcessStartReservation(processID ProcessID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	reservation, exists := e.startReservations[processID]
	if !exists {
		return
	}
	delete(e.startReservations, processID)
	if parentID, child := reservation.relation.ParentID(); child {
		key, _ := reservation.relation.ChildKey()
		identity := childIdentity{parent: parentID, key: key}
		if e.childStartReservations[identity] == processID {
			delete(e.childStartReservations, identity)
		}
	}
}

func (e *Engine) publishReservedProcess(handle *processHandleState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	reservation, exists := e.startReservations[handle.processID]
	if !exists || reservation.relation != handle.relation ||
		reservation.deploymentRef != handle.deploymentRef ||
		reservation.treeLimits != handle.treeLimits || e.closeDone != nil ||
		e.processes[handle.processID] != nil {
		panic("agent: invalid Process start reservation")
	}
	var identity childIdentity
	parentID, isChild := handle.relation.ParentID()
	if isChild {
		key, _ := handle.relation.ChildKey()
		identity = childIdentity{parent: parentID, key: key}
		if e.childStartReservations[identity] != handle.processID ||
			e.children[identity].Valid() {
			panic("agent: invalid child Process start reservation")
		}
	}
	delete(e.startReservations, handle.processID)
	handle.childRequestDigest = reservation.childRequestDigest
	e.processes[handle.processID] = handle
	if handle.relation.IsRoot() {
		if handle.runtime.Load() == nil || e.trees[handle.processID] != nil {
			panic("agent: invalid root tree runtime")
		}
		e.trees[handle.processID] = handle.runtime.Load()
	}
	if isChild {
		parent := e.processes[parentID]
		if parent == nil || handle.runtime.Load() == nil || handle.runtime.Load() != parent.runtime.Load() {
			panic("agent: invalid child tree runtime")
		}
		delete(e.childStartReservations, identity)
		e.children[identity] = handle.processID
	}
}
