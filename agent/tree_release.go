package agent

import "context"

// ReleaseTree waits for the complete root tree to settle and removes its
// in-memory registry entries and execution state. It does not cancel work or
// delete Host persistence. Capture any required TreeSnapshot before releasing.
// Existing Process handles retain their terminal Result and ProcessSnapshot;
// Engine.Process and tree operations no longer find the released identities.
// Canceling ctx before settlement leaves the tree registered and usable.
func (e *Engine) ReleaseTree(ctx context.Context, rootID ProcessID) error {
	if e == nil {
		return ErrEngineClosed
	}
	ctx = requireContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime, err := e.runtimeForTree(rootID)
	if err != nil {
		return err
	}
	select {
	case <-runtime.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	// Settlement may need another tree operation, so acquire exclusive access
	// only after the runtime has stopped.
	operation, err := e.acquireTreeOperation(ctx, rootID)
	if err != nil {
		return err
	}
	defer operation.release()
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.trees[rootID] != runtime {
		return ErrInvalidProcessRelation
	}
	for processID, controller := range e.processes {
		if controller.relation.RootID() != rootID {
			continue
		}
		if parentID, child := controller.relation.ParentID(); child {
			key, _ := controller.relation.ChildKey()
			delete(e.children, childIdentity{parent: parentID, key: key})
		}
		controller.runtime.Store(nil)
		delete(e.processes, processID)
	}
	delete(e.trees, rootID)
	return nil
}
