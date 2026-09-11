package agent

import (
	"context"
	"fmt"
)

type restoredTreeProcess struct {
	snapshot ProcessSnapshot
	handle   *processHandleState
	state    *processState
	wire     processSnapshotWire
}

// RestoreTree recreates a complete Process tree from one strict TreeSnapshot.
// rootDeployment must exactly bind the captured root; same-reference children
// reuse it, while other exact references are resolved through EngineConfig's
// DeploymentResolver. Registration is all-or-nothing within this Engine.
// Committed states and nonterminal prepared candidates must restore through
// their exact Definition before registration, activation, or Effect dispatch.
// Interrupted terminal candidates remain inert evidence and are not restored.
// Both completed and prepared completion outputs must satisfy that Definition's
// output schema before admission.
//
// Snapshot capabilities, limits, budgets, and usage remain authoritative. Current
// EngineConfig start defaults do not revoke or rewrite captured grants. The Host
// must authorize the snapshot before calling RestoreTree; ProcessAdmitter and
// initialization acknowledgment are not repeated for captured Processes.
func (e *Engine) RestoreTree(
	ctx context.Context,
	rootDeployment Deployment,
	snapshot TreeSnapshot,
) (*Process, error) {
	if e == nil {
		return nil, ErrInvalidEngineConfig
	}
	ctx = requireContext(ctx)
	if err := rootDeployment.validateDefinition(); err != nil {
		return nil, err
	}
	wire, err := snapshot.wire()
	if err != nil {
		return nil, err
	}
	previousIncarnation, snapshotIsDurable := snapshot.IncarnationID()
	engineIsDurable := e.durability != nil
	if snapshotIsDurable != engineIsDurable {
		return nil, ErrTreeDurabilityMismatch
	}
	rootSnapshot := snapshotByID(wire.ProcessSnapshots, wire.RootID)
	if !rootSnapshot.Valid() || rootSnapshot.DeploymentRef() != rootDeployment.DeploymentRef() {
		return nil, fmt.Errorf("%w: exact root Deployment does not match", ErrInvalidTreeSnapshot)
	}
	operation, err := e.acquireTreeOperation(ctx, wire.RootID)
	if err != nil {
		return nil, err
	}
	defer operation.release()
	previousDigest := snapshot.Digest()
	restoration := treeRestoration{
		engine:      e,
		wire:        wire,
		deployments: map[DeploymentRef]Deployment{rootDeployment.DeploymentRef(): rootDeployment},
	}
	if err := restoration.prepareProcesses(); err != nil {
		return nil, err
	}
	if err := restoration.prepareChildWaits(); err != nil {
		return nil, err
	}
	if err := e.reserveRestoredTree(&restoration); err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			e.discardRestoredTree(&restoration)
		}
	}()
	if engineIsDurable {
		incarnation, incarnationErr := newTreeIncarnationID()
		if incarnationErr != nil {
			return nil, incarnationErr
		}
		wire.IncarnationID = &incarnation
		prospectiveSnapshot, snapshotErr := newTreeSnapshot(wire)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		activation, activationErr := newTreeActivation(
			previousIncarnation, previousDigest, incarnation, prospectiveSnapshot,
		)
		if activationErr != nil {
			return nil, activationErr
		}
		if activationErr = activateTree(ctx, e.durability, activation); activationErr != nil {
			return nil, activationErr
		}
		restoration.wire = wire
	}
	restoration.prepareRuntime(ctx)
	e.publishRestoredTree(&restoration)
	published = true
	return e.startRestoredTree(ctx, &restoration), nil
}

type treeRestoration struct {
	engine      *Engine
	wire        treeSnapshotWire
	deployments map[DeploymentRef]Deployment
	processes   []restoredTreeProcess
	childWaits  []*childWaitRegistration
	runtime     *treeRuntime
}

func (t *treeRestoration) prepareRuntime(ctx context.Context) {
	states := make([]*processState, 0, len(t.processes))
	for index := range t.processes {
		states = append(states, t.processes[index].state)
	}
	t.runtime = newTreeRuntime(t.engine, t.wire.RootID, ctx, states...)
	if incarnation, durable := treeSnapshotIncarnation(t.wire.IncarnationID); durable {
		snapshot, err := newTreeSnapshot(t.wire)
		if err != nil {
			panic(err)
		}
		t.runtime.establishDurableHead(incarnation, snapshot)
	}
	for _, registration := range t.childWaits {
		t.runtime.childWaits[registration.waitID] = registration
	}
}

func (t *treeRestoration) prepareProcesses() error {
	t.processes = make([]restoredTreeProcess, 0, len(t.wire.ProcessSnapshots))
	for _, processSnapshot := range t.wire.ProcessSnapshots {
		deployment, err := t.deployment(processSnapshot.DeploymentRef())
		if err != nil {
			return err
		}
		handle, state, processWire, err := prepareRestoredProcess(
			t.engine.durability != nil, deployment, processSnapshot,
		)
		if err != nil {
			return fmt.Errorf(
				"%w: restore Process %s: %w", ErrInvalidTreeSnapshot,
				processSnapshot.ProcessID(), err,
			)
		}
		t.processes = append(t.processes, restoredTreeProcess{
			snapshot: processSnapshot, handle: handle, state: state, wire: processWire,
		})
	}
	return nil
}

func (t *treeRestoration) deployment(reference DeploymentRef) (Deployment, error) {
	if deployment := t.deployments[reference]; deployment.Valid() {
		return deployment, nil
	}
	if t.engine.resolver == nil {
		return Deployment{}, fmt.Errorf(
			"%w: no resolver for %s", ErrInvalidTreeSnapshot, reference.Name(),
		)
	}
	deployment, err := resolveDeployment(t.engine.resolver, reference)
	if err != nil {
		return Deployment{}, fmt.Errorf(
			"%w: resolve exact Deployment %s: %w", ErrInvalidTreeSnapshot, reference.Name(), err,
		)
	}
	if !deployment.Valid() || deployment.DeploymentRef() != reference {
		return Deployment{}, fmt.Errorf(
			"%w: resolver returned a mismatched Deployment for %s", ErrInvalidTreeSnapshot, reference.Name(),
		)
	}
	t.deployments[reference] = deployment
	return deployment, nil
}

func (t *treeRestoration) prepareChildWaits() error {
	t.childWaits = make([]*childWaitRegistration, 0, len(t.wire.ChildWaits))
	for _, encoded := range t.wire.ChildWaits {
		spec, err := encoded.Spec.value()
		if err != nil {
			return fmt.Errorf("%w: child wait: %w", ErrInvalidTreeSnapshot, err)
		}
		t.childWaits = append(t.childWaits, &childWaitRegistration{
			parent: encoded.ParentProcessID, waitID: encoded.WaitID, spec: spec,
		})
	}
	return nil
}

func (e *Engine) startRestoredTree(ctx context.Context, restoration *treeRestoration) *Process {
	for index := range restoration.processes {
		entry := &restoration.processes[index]
		if entry.wire.Status.Terminal() {
			entry.handle.publishResult(entry.state.result())
		}
	}
	for index := len(restoration.processes) - 1; index >= 0; index-- {
		entry := &restoration.processes[index]
		if !entry.wire.Status.Terminal() {
			continue
		}
		restoration.runtime.propagateProcessTermination(entry.state)
		entry.handle.finishBookkeeping()
	}
	root := restoration.runtime.processes[restoration.wire.RootID].handle
	go restoration.runtime.run(requireContext(ctx))
	return &Process{handle: root}
}

func (e *Engine) reserveRestoredTree(restoration *treeRestoration) error {
	if restoration == nil || len(restoration.processes) == 0 {
		return ErrInvalidTreeSnapshot
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closeDone != nil {
		return ErrEngineClosed
	}
	rootID := restoration.wire.RootID
	if e.treeRestoreReservations[rootID] != nil || e.trees[rootID] != nil {
		return ErrProcessAlreadyExists
	}
	for _, process := range restoration.processes {
		if _, exists := e.processes[process.handle.processID]; exists {
			return ErrProcessAlreadyExists
		}
		if _, exists := e.startReservations[process.handle.processID]; exists {
			return ErrProcessAlreadyExists
		}
		if e.restoredProcessReserved(process.handle.processID) {
			return ErrProcessAlreadyExists
		}
		if parentID, child := process.handle.relation.ParentID(); child {
			key, _ := process.handle.relation.ChildKey()
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
		}
	}
	for _, wait := range restoration.childWaits {
		if wait == nil || !wait.waitID.Valid() {
			return ErrInvalidChildWait
		}
	}
	e.treeRestoreReservations[rootID] = restoration
	return nil
}

// restoredProcessReserved requires e.mu to be held.
func (e *Engine) restoredProcessReserved(processID ProcessID) bool {
	for _, restoration := range e.treeRestoreReservations {
		for _, process := range restoration.processes {
			if process.handle.processID == processID {
				return true
			}
		}
	}
	return false
}

// restoredChildReserved requires e.mu to be held.
func (e *Engine) restoredChildReserved(identity childIdentity) bool {
	for _, restoration := range e.treeRestoreReservations {
		for _, process := range restoration.processes {
			parentID, child := process.handle.relation.ParentID()
			if !child {
				continue
			}
			key, _ := process.handle.relation.ChildKey()
			if identity == (childIdentity{parent: parentID, key: key}) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) discardRestoredTree(restoration *treeRestoration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if restoration != nil && e.treeRestoreReservations[restoration.wire.RootID] == restoration {
		delete(e.treeRestoreReservations, restoration.wire.RootID)
	}
}

func (e *Engine) publishRestoredTree(restoration *treeRestoration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rootID := restoration.wire.RootID
	if e.closeDone != nil || e.treeRestoreReservations[rootID] != restoration {
		panic("agent: invalid restored tree reservation")
	}
	runtime := restoration.processes[0].handle.runtime.Load()
	if runtime == nil || runtime.rootID != rootID || e.trees[rootID] != nil {
		panic("agent: invalid restored tree runtime")
	}
	for _, process := range restoration.processes {
		handle := process.handle
		if e.processes[handle.processID] != nil ||
			e.startReservations[handle.processID].relation.Valid() {
			panic("agent: restored Process reservation changed")
		}
		e.processes[handle.processID] = handle
		if parentID, child := handle.relation.ParentID(); child {
			key, _ := handle.relation.ChildKey()
			e.children[childIdentity{parent: parentID, key: key}] = handle.processID
		}
	}
	e.trees[rootID] = runtime
	delete(e.treeRestoreReservations, rootID)
}

func snapshotByID(snapshots []ProcessSnapshot, id ProcessID) ProcessSnapshot {
	for _, snapshot := range snapshots {
		if snapshot.ProcessID() == id {
			return snapshot
		}
	}
	return ProcessSnapshot{}
}
