package agent_test

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

// This Host chooses its own I/O bound and shutdown signal. The storage below
// simulates a serializable transaction over encoded rows; production adapters
// must prove the same guarantees against their actual database and transport.
type hostTreeCommitter struct {
	store    *hostTreeStore
	shutdown context.Context
	timeout  time.Duration
}

func (h *hostTreeCommitter) ActivateTree(ctx context.Context, activation agent.TreeActivation) error {
	content, err := activation.ContentDigest()
	if err != nil {
		return err
	}
	return h.commit(ctx, hostCommitWrite{
		identity: activation.Identity(), content: content, snapshot: activation.TreeSnapshot(),
		previous: activation.PreviousTreeDigest(), previousWriter: activation.PreviousIncarnationID(),
	})
}

func (h *hostTreeCommitter) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	content, err := boundary.ContentDigest()
	if err != nil {
		return err
	}
	return h.commit(ctx, hostCommitWrite{
		identity: boundary.Identity(), content: content, snapshot: boundary.TreeSnapshot(),
		previous: boundary.PreviousTreeDigest(), previousWriter: boundary.TreeSnapshot().IncarnationID(),
		sequence: boundary.Sequence(),
	})
}

func (h *hostTreeCommitter) CommitCheckpoint(ctx context.Context, checkpoint agent.TreeCheckpoint) error {
	content, err := checkpoint.ContentDigest()
	if err != nil {
		return err
	}
	return h.commit(ctx, hostCommitWrite{
		identity: checkpoint.Identity(), content: content, snapshot: checkpoint.TreeSnapshot(),
		previous: checkpoint.PreviousTreeDigest(), previousWriter: checkpoint.TreeSnapshot().IncarnationID(),
		sequence: checkpoint.Sequence(),
	})
}

func (h *hostTreeCommitter) commit(ctx context.Context, write hostCommitWrite) error {
	storageContext, cancel := context.WithTimeout(ctx, h.timeout)
	stop := context.AfterFunc(h.shutdown, cancel)
	defer func() {
		stop()
		cancel()
	}()
	if h.shutdown.Err() != nil {
		cancel()
	}
	return h.store.transact(storageContext, write)
}

func (h *hostTreeCommitter) LoadTree(ctx context.Context, rootID agent.ProcessID) (agent.TreeSnapshot, bool, error) {
	return h.store.LoadTree(ctx, rootID)
}

type hostCommitWrite struct {
	identity       string
	content        agent.Digest
	snapshot       agent.TreeSnapshot
	previous       agent.Digest
	previousWriter agent.TreeIncarnationID
	sequence       uint64
}

type hostTreeRow struct {
	snapshot []byte
	sequence uint64
}

type hostTreeStore struct {
	mu     sync.Mutex
	heads  map[agent.ProcessID]hostTreeRow
	facts  map[string]agent.Digest
	before func(context.Context, hostCommitWrite) error
	after  func(context.Context, hostCommitWrite) error
}

func newHostTreeStore() *hostTreeStore {
	return &hostTreeStore{
		heads: make(map[agent.ProcessID]hostTreeRow),
		facts: make(map[string]agent.Digest),
	}
}

func (h *hostTreeStore) LoadTree(ctx context.Context, rootID agent.ProcessID) (agent.TreeSnapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return agent.TreeSnapshot{}, false, err
	}
	h.mu.Lock()
	row, exists := h.heads[rootID]
	h.mu.Unlock()
	if !exists {
		return agent.TreeSnapshot{}, false, nil
	}
	snapshot, err := agent.ParseTreeSnapshot(row.snapshot)
	return snapshot, true, err
}

func (h *hostTreeStore) transact(ctx context.Context, write hostCommitWrite) error {
	if h.before != nil {
		if err := h.before(ctx, write); err != nil {
			return err
		}
	}
	if err := h.apply(ctx, write); err != nil {
		return err
	}
	if h.after != nil {
		return h.after(ctx, write)
	}
	return nil
}

func (h *hostTreeStore) apply(ctx context.Context, write hostCommitWrite) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	row, exists := h.heads[write.snapshot.RootID()]
	var head agent.TreeSnapshot
	if exists {
		var err error
		head, err = agent.ParseTreeSnapshot(row.snapshot)
		if err != nil {
			return err
		}
	}
	if write.sequence > 0 && write.previous.Valid() && (!exists || head.IncarnationID() != write.previousWriter) {
		return agent.ErrTreeIncarnationConflict
	}
	if content, committed := h.facts[write.identity]; committed {
		if content == write.content && row.sequence == write.sequence && head.Digest() == write.snapshot.Digest() {
			return nil
		}
		return agent.ErrCommitConflict
	}
	if !write.previous.Valid() {
		if exists {
			return agent.ErrTreeIncarnationConflict
		}
	} else {
		if !exists || head.Digest() != write.previous || head.IncarnationID() != write.previousWriter {
			return agent.ErrTreeIncarnationConflict
		}
		if write.sequence > 0 && (row.sequence == math.MaxUint64 || write.sequence != row.sequence+1) {
			return agent.ErrTreeIncarnationConflict
		}
	}
	h.facts[write.identity] = write.content
	h.heads[write.snapshot.RootID()] = hostTreeRow{snapshot: write.snapshot.JSON(), sequence: write.sequence}
	return nil
}

// Reconciliation observes both the fact and current head; finding an old fact
// alone cannot authorize an ACK after subsequent progress or writer activation.
func (h *hostTreeStore) reconcile(ctx context.Context, write hostCommitWrite) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	content, committed := h.facts[write.identity]
	row, exists := h.heads[write.snapshot.RootID()]
	if !committed || !exists || content != write.content || row.sequence != write.sequence {
		return false, nil
	}
	head, err := agent.ParseTreeSnapshot(row.snapshot)
	return err == nil && head.Digest() == write.snapshot.Digest(), err
}

func TestHostTreeCommitterConformance(t *testing.T) {
	agenttest.RunTreeCommitterConformance(t, func() agenttest.TreeCommitterConformanceDriver {
		return &hostTreeCommitter{store: newHostTreeStore(), shutdown: t.Context(), timeout: time.Second}
	})
}

func ExampleTreeCommitter() {
	ctx := context.Background()
	shutdown, stop := context.WithCancel(ctx)
	defer stop()
	committer := &hostTreeCommitter{store: newHostTreeStore(), shutdown: shutdown, timeout: time.Second}
	definition, err := newEchoDefinition()
	if err != nil {
		panic(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: echoDispatcher{},
		ImplementationDigest: agent.ComputeDigest([]byte("host-commit-example")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("host-commit-config")),
	})
	if err != nil {
		panic(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: committer})
	if err != nil {
		panic(err)
	}
	input, err := agent.EncodePayload(echoInput{Value: "durable"})
	if err != nil {
		panic(err)
	}
	result, err := engine.Run(ctx, deployment, input)
	if err != nil {
		panic(err)
	}
	head, exists, err := committer.LoadTree(ctx, result.ProcessID())
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Status(), exists && head.Valid())
	if err := engine.Close(ctx); err != nil {
		panic(err)
	}
	// Output:
	// completed true
}

var _ agenttest.TreeCommitterConformanceDriver = (*hostTreeCommitter)(nil)
