package agent

import "context"

// A head generation belongs to one acknowledged checkpoint. Callers retain the
// generation while waiting; the tree never retains individual subscriptions.
type treeHead struct {
	snapshot TreeSnapshot
	advanced chan struct{}
	err      error
}

func (t *treeHead) digest() Digest {
	if t == nil {
		return Digest{}
	}
	return t.snapshot.Digest()
}

func (t *treeHead) await(ctx context.Context, done <-chan struct{}) error {
	if t == nil {
		return ErrTreeDurabilityMismatch
	}
	select {
	case <-t.advanced:
		return t.err
	default:
	}
	select {
	case <-t.advanced:
		return t.err
	case <-done:
		return ErrEngineQuiescenceUnavailable
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Only the tree owner completes a generation, exactly once. Closing advanced
// publishes the outcome to every caller, including callers that start later.
func (t *treeHead) finish(err error) {
	if t != nil {
		t.err = err
		close(t.advanced)
	}
}

func (t *treeRuntime) advanceHead(snapshot TreeSnapshot) {
	if t.head.digest() == snapshot.Digest() {
		return
	}
	previous := t.head
	t.head = &treeHead{snapshot: snapshot, advanced: make(chan struct{})}
	for _, snapshot := range snapshot.ProcessSnapshots() {
		if process := t.processes[snapshot.ProcessID()]; process != nil {
			process.handle.updateStatus(snapshot.status)
		}
	}
	previous.finish(nil)
}
