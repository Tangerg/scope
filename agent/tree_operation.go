package agent

import (
	"context"
	"sync"
)

type treeOperation struct {
	engine   *Engine
	rootID   ProcessID
	released chan struct{}
	once     sync.Once
}

func (e *Engine) acquireTreeOperation(
	ctx context.Context,
	rootID ProcessID,
) (*treeOperation, error) {
	ctx = requireContext(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e.treeOperationsMu.Lock()
		active := e.treeOperations[rootID]
		if active == nil {
			operation := &treeOperation{
				engine: e, rootID: rootID, released: make(chan struct{}),
			}
			e.treeOperations[rootID] = operation
			e.treeOperationsMu.Unlock()
			return operation, nil
		}
		released := active.released
		e.treeOperationsMu.Unlock()
		select {
		case <-released:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (t *treeOperation) release() {
	if t == nil || t.engine == nil {
		return
	}
	t.once.Do(func() {
		engine := t.engine
		engine.treeOperationsMu.Lock()
		if engine.treeOperations[t.rootID] == t {
			delete(engine.treeOperations, t.rootID)
		}
		close(t.released)
		engine.treeOperationsMu.Unlock()
	})
}
