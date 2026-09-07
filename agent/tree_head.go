package agent

import "context"

// A head generation belongs to one acknowledged checkpoint. Callers retain the
// generation while waiting; the tree never retains individual subscriptions.
type treeHead struct {
	value    Digest
	advanced chan struct{}
	err      error
}

func (t *treeHead) digest() Digest {
	if t == nil {
		return Digest{}
	}
	return t.value
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

func (t *treeRuntime) advanceHead(digest Digest) {
	if t.head.digest() == digest {
		return
	}
	previous := t.head
	t.head = &treeHead{value: digest, advanced: make(chan struct{})}
	previous.finish(nil)
}
