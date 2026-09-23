package agent

import (
	"context"
	"fmt"
)

type restoredTreeProcess struct {
	snapshot ProcessSnapshot
	handle   *processHandle
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

// prepare owns every decision about whether this tree can be rebuilt: it
// resolves each captured binding, restores every committed and prepared
// Execution through its own Definition, and admits the captured snapshot
// capacity. It works entirely in memory, so a discarded attempt leaves no
// Engine state behind and the same pass can answer a question or begin a
// restoration.
func (t *treeRestoration) prepare(ctx context.Context) (TreeIncarnationID, error) {
	if err := t.prepareProcesses(ctx); err != nil {
		return TreeIncarnationID{}, err
	}
	if err := t.prepareChildWaits(); err != nil {
		return TreeIncarnationID{}, err
	}
	incarnation := newTreeIncarnationID()
	if err := t.prepareRuntime(ctx, incarnation); err != nil {
		return TreeIncarnationID{}, fmt.Errorf("%w: snapshot capacity: %w", ErrInvalidTreeSnapshot, err)
	}
	return incarnation, nil
}

func (t *treeRestoration) prepareRuntime(ctx context.Context, incarnation TreeIncarnationID) error {
	states := make([]*processState, 0, len(t.processes))
	for index := range t.processes {
		states = append(states, t.processes[index].state)
	}
	t.runtime = newTreeRuntime(t.engine, t.wire.RootID, ctx, states...)
	t.runtime.incarnation = incarnation
	t.runtime.childWaits = t.childWaits
	return t.runtime.validateSnapshotCapacity()
}

func (t *treeRestoration) prepareProcesses(ctx context.Context) error {
	t.processes = make([]restoredTreeProcess, 0, len(t.wire.ProcessSnapshots))
	for _, processSnapshot := range t.wire.ProcessSnapshots {
		deployment, err := t.deployment(processSnapshot.DeploymentRef())
		if err != nil {
			return err
		}
		handle, state, processWire, err := prepareRestoredProcess(ctx,
			deployment, processSnapshot,
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
