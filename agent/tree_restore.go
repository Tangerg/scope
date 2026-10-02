package agent

import (
	"context"
	"fmt"
)

// treeRestoration rebuilds one captured tree. Its prepared runtime owns the
// restored members, whose mailboxes carry their open child waits.
type treeRestoration struct {
	engine      *Engine
	wire        treeSnapshotWire
	deployments map[DeploymentRef]Deployment
	runtime     *treeRuntime
}

// Validation and restoration share this pure pass without invoking admission policy.
func (t *treeRestoration) prepare(ctx context.Context) error {
	processes, err := t.prepareProcesses(ctx)
	if err != nil {
		return err
	}
	t.runtime = newTreeRuntime(t.engine, t.wire.RootID, t.wire.TreeLimits, ctx, processes...)
	if err := t.runtime.validateSnapshotCapacity(); err != nil {
		return fmt.Errorf("%w: snapshot capacity: %w", ErrInvalidTreeSnapshot, err)
	}
	return nil
}

func (t *treeRestoration) prepareProcesses(ctx context.Context) ([]*processState, error) {
	processes := make([]*processState, 0, len(t.wire.ProcessSnapshots))
	for _, processSnapshot := range t.wire.ProcessSnapshots {
		deployment, err := t.deployment(processSnapshot.DeploymentRef())
		if err != nil {
			return nil, err
		}
		process, err := prepareRestoredProcess(ctx, deployment, processSnapshot)
		if err != nil {
			return nil, fmt.Errorf(
				"%w: restore Process %s: %w", ErrInvalidTreeSnapshot,
				processSnapshot.ProcessID(), err,
			)
		}
		processes = append(processes, process)
	}
	return processes, nil
}

func (t *treeRestoration) deployment(reference DeploymentRef) (Deployment, error) {
	if deployment, bound := t.deployments[reference]; bound {
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
	if err := t.bind(deployment); err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

// bind makes a validated deployment and, transitively, its static child
// bindings available to the captured Processes they may have started. Each
// child's live Definition is validated once, when it is first bound. A
// reference is exact identity, so the first binding of each reference serves
// every Process.
func (t *treeRestoration) bind(deployment Deployment) error {
	t.deployments[deployment.reference] = deployment
	for _, child := range deployment.children {
		if _, bound := t.deployments[child.reference]; bound {
			continue
		}
		if err := child.validateDefinition(); err != nil {
			return fmt.Errorf("%w: bound Deployment %s: %w", ErrInvalidTreeSnapshot, child.reference.Name(), err)
		}
		if err := t.bind(child); err != nil {
			return err
		}
	}
	return nil
}
