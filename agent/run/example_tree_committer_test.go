package run_test

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

// This Host chooses its own I/O bound and shutdown signal. The storage below
// simulates a serializable transaction over encoded rows; production adapters
// must prove the same guarantees against their actual database and transport.
type hostTreeCommitter struct {
	store    *hostTreeStore
	shutdown context.Context
	timeout  time.Duration
}

func (h *hostTreeCommitter) ActivateTree(ctx context.Context, activation run.TreeActivation) error {
	content, err := activation.ContentDigest()
	if err != nil {
		return err
	}
	return h.commit(ctx, hostCommitWrite{
		identity: activation.Identity(), content: content, snapshot: activation.TreeSnapshot(),
		previous: activation.PreviousTreeDigest(), previousWriter: activation.PreviousIncarnationID(),
	})
}

func (h *hostTreeCommitter) CommitEffect(ctx context.Context, boundary run.EffectBoundary) error {
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

func (h *hostTreeCommitter) CommitCheckpoint(ctx context.Context, checkpoint run.TreeCheckpoint) error {
	content, err := checkpoint.ContentDigest()
	if err != nil {
		return err
	}
	start, _ := checkpoint.Start()
	return h.commit(ctx, hostCommitWrite{start: start,
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

func (h *hostTreeCommitter) LoadTree(ctx context.Context, rootID run.ProcessID) (run.TreeSnapshot, bool, error) {
	return h.store.LoadTree(ctx, rootID)
}

type hostCommitWrite struct {
	start          run.StartReceipt
	identity       string
	content        run.Digest
	snapshot       run.TreeSnapshot
	previous       run.Digest
	previousWriter run.TreeIncarnationID
	sequence       uint64
}

type hostTreeRow struct {
	snapshot []byte
	sequence uint64
}

type hostTreeStore struct {
	starts map[run.StartID]run.StartReceipt
	mu     sync.Mutex
	heads  map[run.ProcessID]hostTreeRow
	facts  map[string]run.Digest
	before func(context.Context, hostCommitWrite) error
	after  func(context.Context, hostCommitWrite) error
}

func newHostTreeStore() *hostTreeStore {
	return &hostTreeStore{starts: make(map[run.StartID]run.StartReceipt),
		heads: make(map[run.ProcessID]hostTreeRow),
		facts: make(map[string]run.Digest),
	}
}

func (h *hostTreeStore) LoadTree(ctx context.Context, rootID run.ProcessID) (run.TreeSnapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return run.TreeSnapshot{}, false, err
	}
	h.mu.Lock()
	row, exists := h.heads[rootID]
	h.mu.Unlock()
	if !exists {
		return run.TreeSnapshot{}, false, nil
	}
	snapshot, err := run.ParseTreeSnapshot(row.snapshot)
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
	if write.start.Valid() {
		if prev, ok := h.starts[write.start.ID]; ok {
			if prev.Intent != write.start.Intent {
				return &run.StartConflictError{Receipt: prev}
			}
			if prev.RootID != write.start.RootID {
				return &run.StartAcceptedError{Receipt: prev}
			}
		}
	}
	row, exists := h.heads[write.snapshot.RootID()]
	var head run.TreeSnapshot
	if exists {
		var err error
		head, err = run.ParseTreeSnapshot(row.snapshot)
		if err != nil {
			return err
		}
	}
	if write.sequence > 0 && write.previous.Valid() && (!exists || head.IncarnationID() != write.previousWriter) {
		return run.ErrTreeIncarnationConflict
	}
	if content, committed := h.facts[write.identity]; committed {
		if content == write.content && row.sequence == write.sequence && head.Digest() == write.snapshot.Digest() {
			return nil
		}
		return run.ErrCommitConflict
	}
	if !write.previous.Valid() {
		if exists {
			return run.ErrTreeIncarnationConflict
		}
	} else {
		if !exists || head.Digest() != write.previous || head.IncarnationID() != write.previousWriter {
			return run.ErrTreeIncarnationConflict
		}
		if write.sequence > 0 && (row.sequence == math.MaxUint64 || write.sequence != row.sequence+1) {
			return run.ErrTreeIncarnationConflict
		}
	}
	if write.start.Valid() {
		h.starts[write.start.ID] = write.start
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
	head, err := run.ParseTreeSnapshot(row.snapshot)
	return err == nil && head.Digest() == write.snapshot.Digest(), err
}

func TestHostTreeCommitterConformance(t *testing.T) {
	runtest.RunTreeCommitterConformance(t, func() runtest.TreeCommitterConformanceDriver {
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
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: echoDispatcher{},
		ImplementationDigest: run.ComputeDigest([]byte("host-commit-example")),
		ConfigurationDigest:  run.ComputeDigest([]byte("host-commit-config")),
	})
	if err != nil {
		panic(err)
	}
	engine, err := run.New(context.Background(), run.Config{Store: committer})
	if err != nil {
		panic(err)
	}
	input, err := run.EncodePayload(echoInput{Value: "durable"})
	if err != nil {
		panic(err)
	}
	result, err := runtest.Observe(ctx, engine, deployment, input)
	if err != nil {
		panic(err)
	}
	head, exists, err := committer.LoadTree(ctx, result.ProcessID())
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Termination().Status(), exists && head.Valid())
	if err := engine.Close(ctx); err != nil {
		panic(err)
	}
	// Output:
	// completed true
}

var _ runtest.TreeCommitterConformanceDriver = (*hostTreeCommitter)(nil)

func (h *hostTreeCommitter) LookupStart(_ context.Context, id run.StartID) (run.StartReceipt, bool, error) {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	v, ok := h.store.starts[id]
	return v, ok, nil
}
