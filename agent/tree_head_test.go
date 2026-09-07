package agent

import (
	"context"
	"errors"
	"testing"
)

func TestTreeHeadCancellationDoesNotAffectOtherCallers(t *testing.T) {
	runtime := &treeRuntime{}
	first := completedTreeSnapshot(t)
	runtime.advanceHead(first)
	head := runtime.head
	for range 128 {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := head.await(ctx, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled await = %v", err)
		}
	}
	runtime.advanceHead(first)
	select {
	case <-head.advanced:
		t.Fatal("acknowledging the same head woke its callers")
	default:
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- head.await(t.Context(), nil) }()
	}
	second := completedTreeSnapshot(t)
	runtime.advanceHead(second)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("peer await = %v", err)
		}
	}
	if err := head.await(t.Context(), nil); err != nil {
		t.Fatalf("await after advancement = %v", err)
	}
}

func TestTreeHeadPreservesFailureAndTreeTermination(t *testing.T) {
	errCommit := errors.New("commit rejected")
	head := &treeHead{advanced: make(chan struct{})}
	head.finish(errCommit)
	if err := head.await(t.Context(), nil); !errors.Is(err, errCommit) {
		t.Fatalf("failed await = %v", err)
	}
	done := make(chan struct{})
	close(done)
	head = &treeHead{advanced: make(chan struct{})}
	if err := head.await(t.Context(), done); !errors.Is(err, ErrEngineQuiescenceUnavailable) {
		t.Fatalf("terminated await = %v", err)
	}
	var absent *treeHead
	if err := absent.await(t.Context(), nil); !errors.Is(err, ErrTreeDurabilityMismatch) {
		t.Fatalf("non-durable await = %v", err)
	}
}
