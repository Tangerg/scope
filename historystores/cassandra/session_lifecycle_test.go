package cassandra_test

import (
	"errors"
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/historystores/cassandra"
)

func TestClosedSessionErrorsArePreserved(t *testing.T) {
	session := new(gocql.Session)
	store, err := cassandra.NewStore(t.Context(), cassandra.StoreConfig{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	// Closing establishes the SDK's terminal error path without server I/O.
	session.Close()

	t.Run("schema initialization", func(t *testing.T) {
		created, err := cassandra.NewStore(t.Context(), cassandra.StoreConfig{Session: session, InitializeSchema: true})
		if created != nil || !errors.Is(err, gocql.ErrSessionClosed) {
			t.Fatalf("NewStore = %v, %v; want no store and the session error", created, err)
		}
	})
	t.Run("append", func(t *testing.T) {
		batch, err := history.NewBatch("conversation", "append", chat.NewUserMessage(chat.NewTextPart("message")))
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := store.Append(t.Context(), batch)
		if receipt != (history.Receipt{}) || !errors.Is(err, gocql.ErrSessionClosed) {
			t.Fatalf("Append = %v, %v; want no receipt and the session error", receipt, err)
		}
	})
	t.Run("read", func(t *testing.T) {
		messages, err := store.Read(t.Context(), "conversation")
		if messages != nil || !errors.Is(err, gocql.ErrSessionClosed) {
			t.Fatalf("Read = %v, %v; want no snapshot and the session error", messages, err)
		}
	})
	t.Run("clear", func(t *testing.T) {
		if err := store.Clear(t.Context(), "conversation"); !errors.Is(err, gocql.ErrSessionClosed) {
			t.Fatalf("Clear error = %v, want the session error", err)
		}
	})
	t.Run("list", func(t *testing.T) {
		conversations, err := store.Conversations(t.Context())
		if conversations != nil || !errors.Is(err, gocql.ErrSessionClosed) {
			t.Fatalf("Conversations = %v, %v; want no result and the session error", conversations, err)
		}
	})
}
