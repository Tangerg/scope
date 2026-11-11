package storetest

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

const cleanupTimeout = 30 * time.Second

// RunAtomic exercises a configured store's append identity and closure boundary.
// Unlike Run, it performs real I/O. Every check uses a fresh conversation and
// closes it afterward; the closure and receipt evidence must remain durable.
func RunAtomic(t *testing.T, store history.Store) {
	t.Helper()
	t.Run("ConcurrentRetriesAppendExactlyOnce", func(t *testing.T) { checkConcurrentRetries(t, store) })
	t.Run("ConflictingIdentityAndDistinctIntent", func(t *testing.T) { checkAppendIdentity(t, store) })
	t.Run("ConcurrentBatchesNeverInterleave", func(t *testing.T) { checkBatchAtomicity(t, store) })
	t.Run("ClearFencesConcurrentAndPreviouslyAcceptedAppends", func(t *testing.T) { checkClearFence(t, store) })
	t.Run("ClearClosesAnAbsentIdentity", func(t *testing.T) { checkClearAbsent(t, store) })
}

func checkConcurrentRetries(t *testing.T, store history.Store) {
	conversation := atomicConversation(t, store)
	batch := atomicBatch(t, conversation, "append", "first", "second")
	retries := make([]func() error, 16)
	for index := range retries {
		retries[index] = func() error { return appendWithReceipt(t.Context(), store, batch) }
	}
	for _, err := range concurrently(retries...) {
		if err != nil {
			t.Error(err)
		}
	}
	atomicRead(t, store, conversation, batch.Messages())
}

func checkAppendIdentity(t *testing.T, store history.Store) {
	conversation := atomicConversation(t, store)
	batch := atomicBatch(t, conversation, "append", "original")
	atomicAppend(t, store, batch)
	changed := atomicBatch(t, conversation, "append", "changed")
	if receipt, err := store.Append(t.Context(), changed); !errors.Is(err, history.ErrAppendConflict) || receipt != (history.Receipt{}) {
		t.Fatalf("conflicting identity = %+v, %v", receipt, err)
	}
	distinct := atomicBatch(t, conversation, "another", "original")
	atomicAppend(t, store, distinct)
	atomicRead(t, store, conversation, append(batch.Messages(), distinct.Messages()...))
}

func checkBatchAtomicity(t *testing.T, store history.Store) {
	conversation := atomicConversation(t, store)
	first := atomicBatch(t, conversation, "first", "first.1", "first.2", "first.3")
	second := atomicBatch(t, conversation, "second", "second.1", "second.2", "second.3")
	for _, err := range concurrently(
		func() error { return appendWithReceipt(t.Context(), store, first) },
		func() error { return appendWithReceipt(t.Context(), store, second) },
	) {
		if err != nil {
			t.Error(err)
		}
	}
	messages, err := store.Read(t.Context(), conversation)
	if err != nil {
		t.Fatal(err)
	}
	forward := append(first.Messages(), second.Messages()...)
	reverse := append(second.Messages(), first.Messages()...)
	if !reflect.DeepEqual(messages, forward) && !reflect.DeepEqual(messages, reverse) {
		t.Fatalf("concurrent append interleaved batches: %v", messages)
	}
}

func checkClearFence(t *testing.T, store history.Store) {
	conversation := atomicConversation(t, store)
	accepted := atomicBatch(t, conversation, "accepted", "original")
	atomicAppend(t, store, accepted)
	pending := atomicBatch(t, conversation, "pending", "later")
	errs := concurrently(
		func() error { return appendWithReceipt(t.Context(), store, pending) },
		func() error { return store.Clear(t.Context(), conversation) },
	)
	if err := errs[0]; err != nil && !errors.Is(err, history.ErrConversationClosed) {
		t.Errorf("concurrent append: %v", err)
	}
	if err := errs[1]; err != nil {
		t.Fatal(err)
	}
	atomicClosed(t, store, accepted)
	atomicClosed(t, store, pending)
	atomicClosed(t, store, atomicBatch(t, conversation, "fresh", "new"))
	atomicRead(t, store, conversation, []chat.Message{})
	if lister, ok := store.(history.Lister); ok {
		ids, err := lister.Conversations(t.Context())
		if err != nil || slices.Contains(ids, conversation) {
			t.Fatalf("closed conversation remains listed: %v, %v", ids, err)
		}
	}
}

func checkClearAbsent(t *testing.T, store history.Store) {
	conversation := atomicConversation(t, store)
	if err := store.Clear(t.Context(), conversation); err != nil {
		t.Fatal(err)
	}
	atomicClosed(t, store, atomicBatch(t, conversation, "first", "delayed"))
	atomicRead(t, store, conversation, []chat.Message{})
}

// concurrently releases every operation at once, so each check races the
// store's own synchronization rather than goroutine start-up order.
func concurrently(operations ...func() error) []error {
	start := make(chan struct{})
	errs := make([]error, len(operations))
	var group sync.WaitGroup
	for index, operation := range operations {
		group.Go(func() {
			<-start
			errs[index] = operation()
		})
	}
	close(start)
	group.Wait()
	return errs
}

func appendWithReceipt(ctx context.Context, store history.Writer, batch history.Batch) error {
	receipt, err := store.Append(ctx, batch)
	if err != nil {
		return err
	}
	return receipt.ValidateFor(batch)
}

func atomicConversation(t *testing.T, store history.Clearer) history.ConversationID {
	t.Helper()
	id := history.ConversationID("conformance-" + rand.Text())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), cleanupTimeout)
		defer cancel()
		if err := store.Clear(ctx, id); err != nil {
			t.Errorf("close test conversation: %v", err)
		}
	})
	return id
}

func atomicBatch(t *testing.T, conversation history.ConversationID, id history.AppendID, texts ...string) history.Batch {
	t.Helper()
	messages := make([]chat.Message, len(texts))
	for index, text := range texts {
		messages[index] = chat.NewUserMessage(chat.NewTextPart(text))
	}
	batch, err := history.NewBatch(conversation, id, messages...)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func atomicAppend(t *testing.T, store history.Writer, batch history.Batch) {
	t.Helper()
	if err := appendWithReceipt(t.Context(), store, batch); err != nil {
		t.Fatalf("append %s: %v", batch.ID(), err)
	}
}

func atomicClosed(t *testing.T, store history.Writer, batch history.Batch) {
	t.Helper()
	receipt, err := store.Append(t.Context(), batch)
	if !errors.Is(err, history.ErrConversationClosed) || receipt != (history.Receipt{}) {
		t.Fatalf("append after closure: receipt=%+v, error=%v", receipt, err)
	}
}

func atomicRead(t *testing.T, store history.Reader, conversation history.ConversationID, want []chat.Message) {
	t.Helper()
	got, err := store.Read(t.Context(), conversation)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read = %v, %v; want %v", got, err, want)
	}
}
