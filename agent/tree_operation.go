package agent

import (
	"sync"
)

type treeOperation struct {
	engine   *Engine
	rootID   ProcessID
	released chan struct{}
	once     sync.Once
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
