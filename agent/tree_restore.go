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

type treeRestoration struct {
	engine      *Engine
	wire        treeSnapshotWire
	deployments map[DeploymentRef]Deployment
	processes   []restoredTreeProcess
	childWaits  map[ProcessID]map[WaitID]*childWaitRegistration
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
	t.runtime.childWaits = t.childWaits
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
	t.childWaits = make(map[ProcessID]map[WaitID]*childWaitRegistration)
	for _, encoded := range t.wire.ChildWaits {
		spec, err := encoded.Spec.value()
		if err != nil {
			return fmt.Errorf("%w: child wait: %w", ErrInvalidTreeSnapshot, err)
		}
		if t.childWaits[encoded.ParentProcessID] == nil {
			t.childWaits[encoded.ParentProcessID] = make(map[WaitID]*childWaitRegistration)
		}
		t.childWaits[encoded.ParentProcessID][encoded.WaitID] = &childWaitRegistration{waitID: encoded.WaitID, spec: spec}
	}
	return nil
}

func snapshotByID(snapshots []ProcessSnapshot, id ProcessID) ProcessSnapshot {
	for _, snapshot := range snapshots {
		if snapshot.ProcessID() == id {
			return snapshot
		}
	}
	return ProcessSnapshot{}
}
