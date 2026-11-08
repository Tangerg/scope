package storetest

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"slices"
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
	t.Run("ConcurrentRetriesAppendExactlyOnce", func(t *testing.T) {
		conversation := atomicConversation(t, store)
		batch := atomicBatch(t, conversation, "append", "first", "second")
		const attempts = 16
		start := make(chan struct{})
		results := make(chan error, attempts)
		for range attempts {
			go func() {
				<-start
				receipt, err := store.Append(t.Context(), batch)
				if err == nil {
					err = receipt.ValidateFor(batch)
				}
				results <- err
			}()
		}
		close(start)
		for range attempts {
			if err := <-results; err != nil {
				t.Error(err)
			}
		}
		atomicRead(t, store, conversation, batch.Messages())
	})
	t.Run("ConflictingIdentityAndDistinctIntent", func(t *testing.T) {
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
	})
	t.Run("ConcurrentBatchesNeverInterleave", func(t *testing.T) {
		conversation := atomicConversation(t, store)
		first := atomicBatch(t, conversation, "first", "first.1", "first.2", "first.3")
		second := atomicBatch(t, conversation, "second", "second.1", "second.2", "second.3")
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, batch := range []history.Batch{first, second} {
			go func() {
				<-start
				receipt, err := store.Append(t.Context(), batch)
				if err == nil {
					err = receipt.ValidateFor(batch)
				}
				results <- err
			}()
		}
		close(start)
		for range 2 {
			if err := <-results; err != nil {
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
	})
	t.Run("ClearFencesConcurrentAndPreviouslyAcceptedAppends", func(t *testing.T) {
		conversation := atomicConversation(t, store)
		accepted := atomicBatch(t, conversation, "accepted", "original")
		atomicAppend(t, store, accepted)
		pending := atomicBatch(t, conversation, "pending", "later")
		start := make(chan struct{})
		appended := make(chan error, 1)
		cleared := make(chan error, 1)
		go func() {
			<-start
			receipt, err := store.Append(t.Context(), pending)
			if err == nil {
				err = receipt.ValidateFor(pending)
			}
			appended <- err
		}()
		go func() {
			<-start
			cleared <- store.Clear(t.Context(), conversation)
		}()
		close(start)
		if err := <-appended; err != nil && !errors.Is(err, history.ErrConversationClosed) {
			t.Errorf("concurrent append: %v", err)
		}
		if err := <-cleared; err != nil {
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
	})
	t.Run("ClearClosesAnAbsentIdentity", func(t *testing.T) {
		conversation := atomicConversation(t, store)
		if err := store.Clear(t.Context(), conversation); err != nil {
			t.Fatal(err)
		}
		atomicClosed(t, store, atomicBatch(t, conversation, "first", "delayed"))
		atomicRead(t, store, conversation, []chat.Message{})
	})
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
	receipt, err := store.Append(t.Context(), batch)
	if err != nil || receipt.ValidateFor(batch) != nil {
		t.Fatalf("append %s: %+v, %v", batch.ID(), receipt, err)
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
