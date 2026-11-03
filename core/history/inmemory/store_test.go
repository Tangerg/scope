package inmemory_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/inmemory"
	"github.com/Tangerg/scope/core/metadata"
)

func TestStoreOwnsMessages(t *testing.T) {
	var store inmemory.Store
	message := chat.NewUserMessage(chat.NewTextPart("original"))
	message.Metadata = metadata.Map{}
	if err := message.Metadata.Set("turn", 1); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Append(t.Context(), testBatch(t, "conversation", "seed", message)); err != nil {
		t.Fatal(err)
	}
	message.Parts[0].Text = "mutated"
	message.Metadata["turn"][0] = '9'

	read, err := store.Read(t.Context(), "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 1 || read[0].Text() != "original" || string(read[0].Metadata["turn"]) != "1" {
		t.Fatalf("Read = %#v", read)
	}
	read[0].Parts[0].Text = "mutated read"
	readAgain, err := store.Read(t.Context(), "conversation")
	if err != nil || readAgain[0].Text() != "original" {
		t.Fatalf("second Read = %#v, %v", readAgain, err)
	}

	if ids, conversationsErr := store.Conversations(t.Context()); conversationsErr != nil || !slices.Equal(ids, []history.ConversationID{"conversation"}) {
		t.Fatalf("Conversations = %v, %v", ids, conversationsErr)
	}
	if err := store.Clear(t.Context(), "conversation"); err != nil {
		t.Fatal(err)
	}
	if read, err := store.Read(t.Context(), "conversation"); err != nil || read == nil || len(read) != 0 {
		t.Fatalf("cleared Read = %#v, %v", read, err)
	}
}

func TestStoreRejectsInvalidInputAndCancellation(t *testing.T) {
	store := new(inmemory.Store)
	if _, err := store.Append(t.Context(), history.Batch{}); !errors.Is(err, history.ErrInvalidBatch) {
		t.Fatalf("invalid batch error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Read(ctx, "conversation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read cancellation = %v", err)
	}
	if _, err := store.Append(ctx, history.Batch{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append cancellation = %v", err)
	}
	if err := store.Clear(ctx, "conversation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Clear cancellation = %v", err)
	}
}

func TestClearClosesAnUnwrittenIdentity(t *testing.T) {
	var store inmemory.Store
	if err := store.Clear(t.Context(), "unwritten"); err != nil {
		t.Fatal(err)
	}
	batch := testBatch(t, "unwritten", "delayed", chat.NewUserMessage(chat.NewTextPart("message")))
	if receipt, err := store.Append(t.Context(), batch); !errors.Is(err, history.ErrConversationClosed) || receipt != (history.Receipt{}) {
		t.Fatal(receipt, err)
	}
	ids, err := store.Conversations(t.Context())
	if err != nil || ids == nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
}

type cancelAfterCheckContext struct {
	context.Context
	cancel context.CancelFunc
}

func (c cancelAfterCheckContext) Err() error {
	err := c.Context.Err()
	c.cancel()
	return err
}

func TestStoreCancellationBetweenAdmissionAndAccess(t *testing.T) {
	for _, operation := range []string{"append", "clear", "read", "list"} {
		t.Run(operation, func(t *testing.T) {
			store := new(inmemory.Store)
			message := chat.NewUserMessage(chat.NewTextPart("original"))
			if _, err := store.Append(t.Context(), testBatch(t, "conversation", "seed", message)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			checked := cancelAfterCheckContext{Context: ctx, cancel: cancel}
			var err error
			switch operation {
			case "append":
				var outcome history.Receipt
				outcome, err = store.Append(checked, testBatch(t, "conversation", "next", message))
				if outcome != (history.Receipt{}) {
					t.Fatalf("canceled append receipt = %+v", outcome)
				}
			case "clear":
				err = store.Clear(checked, "conversation")
			case "read":
				_, err = store.Read(checked, "conversation")
			case "list":
				_, err = store.Conversations(checked)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("operation after cancellation = %v", err)
			}
			got, err := store.Read(t.Context(), "conversation")
			if err != nil || len(got) != 1 || got[0].Text() != "original" {
				t.Fatalf("cancellation changed history: %v, %v", got, err)
			}
		})
	}
}

func testBatch(t testing.TB, conversation history.ConversationID, id history.AppendID, messages ...chat.Message) history.Batch {
	t.Helper()
	batch, err := history.NewBatch(conversation, id, messages...)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}
