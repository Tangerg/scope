//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestConcurrentStartCommitsOneReceiptHeadAndDispatch(t *testing.T) {
	f := newPostgresFixture(t)
	a := f.committer
	b := &postgresCommitter{pool: a.pool, schema: f.schema, shutdown: t.Context(), timeout: storageTimeout}
	// Both runtimes must finish the preflight read before either writes. Otherwise
	// an early winner can hide a broken unique-key race in the first transaction.
	var arrived sync.WaitGroup
	arrived.Add(2)
	gate := func(ctx context.Context, cut commitCut, write postgresWrite) error {
		if cut == cutBeforeTransaction && write.sequence == 1 {
			arrived.Done()
			arrived.Wait()
		}
		return nil
	}
	a.gate, b.gate = gate, gate
	ar, p, input := newProbeRuntime(t, a, f)
	br, _, _ := newProbeRuntime(t, b, f)
	id := run.StartID{Scope: "tenant-a", Key: "original-request"}
	type admission struct {
		h   *run.Handle[run.Payload]
		err error
	}
	results := make(chan admission, 2)
	for _, rt := range []*run.Runtime{ar, br} {
		go func() {
			h, err := rt.Start(t.Context(), p, run.StartRequest[run.Payload]{ID: id, Input: input})
			results <- admission{h, err}
		}()
	}
	var winner *run.Handle[run.Payload]
	var accepted *run.StartAcceptedError
	for range 2 {
		result := <-results
		if result.err == nil && result.h != nil && winner == nil {
			winner = result.h
			continue
		}
		if result.h != nil || !errors.As(result.err, &accepted) {
			t.Fatalf("unexpected admission: %+v", result)
		}
	}
	if winner == nil || accepted == nil || accepted.Receipt.RootID != winner.Relation().RootID() {
		t.Fatalf("winner %v; duplicate %v", winner, accepted)
	}
	if err := winner.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	receipt, ok, err := a.LookupStart(t.Context(), id)
	if err != nil || !ok || receipt != accepted.Receipt {
		t.Fatalf("receipt after head advancement: %+v %t %v", receipt, ok, err)
	}
	changed, err := run.EncodePayload(probeValue{Value: "changed"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := ar.Start(t.Context(), p, run.StartRequest[run.Payload]{ID: id, Input: changed})
	if _, ok := errors.AsType[*run.StartConflictError](err); h != nil || !ok {
		t.Fatalf("changed intent: %v %v", h, err)
	}
	var heads, receipts, dispatches int
	err = a.pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM tree_heads),(SELECT count(*) FROM start_receipts),(SELECT count(*) FROM external_calls)").Scan(&heads, &receipts, &dispatches)
	if err != nil || heads != 1 || receipts != 1 || dispatches != 1 {
		t.Fatalf("heads=%d receipts=%d dispatches=%d err=%v", heads, receipts, dispatches, err)
	}
}
