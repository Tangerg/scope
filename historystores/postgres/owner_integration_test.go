//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/historystores/postgres"
)

func nativePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SCOPE_HISTORY_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SCOPE_HISTORY_POSTGRES_DSN is required")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func nativeTable(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	table := "history_" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+pgx.Identifier{"public", table}.Sanitize()+"; DROP TABLE IF EXISTS "+pgx.Identifier{"public", table + "_conversations"}.Sanitize()); err != nil {
			t.Error(err)
		}
	})
	return table
}

func TestNativeCanonicalMessageWire(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  json.RawMessage
		text string
	}{
		{name: "number_spelling", raw: json.RawMessage(`{"n":1e3,"exact":9007199254740993}`), text: "hello"},
		{name: "unbounded_number", raw: json.RawMessage(`1e1000000`), text: "hello"},
		{name: "nul_metadata", raw: json.RawMessage(`"\u0000"`), text: "hello"},
		{name: "nul_text", raw: json.RawMessage(`{}`), text: "hello\x00world"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := nativePool(t)
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, TableName: nativeTable(t, pool), InitializeSchema: true})
			if err != nil {
				t.Fatal(err)
			}
			message := chat.NewUserMessage(chat.NewTextPart(test.text))
			message.Metadata = metadata.Map{"value": test.raw}
			message.Parts[0].Metadata = metadata.Map{"value": test.raw}
			if validationErr := message.Validate(); validationErr != nil {
				t.Fatal(validationErr)
			}
			batch := newBatch(t, message)
			receipt, err := store.Append(t.Context(), batch)
			if err != nil || receipt.ValidateFor(batch) != nil {
				t.Fatalf("canonical write: receipt=%+v error=%v", receipt, err)
			}
			messages, err := store.Read(t.Context(), history.ConversationID("conversation"))
			restored, batchErr := history.NewBatch("conversation", "append", messages...)
			if err != nil || batchErr != nil || restored.Digest() != batch.Digest() {
				t.Fatalf("canonical round trip: messages=%+v error=%v, want=%+v", messages, err, message)
			}
		})
	}
}

func TestNativeAppendRequiresItsInsertAcknowledgment(t *testing.T) {
	pool := nativePool(t)
	table := nativeTable(t, pool)
	store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, TableName: table, InitializeSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	first := chat.NewUserMessage(chat.NewTextPart("first"))
	skipped := chat.NewUserMessage(chat.NewTextPart("skipped"))
	batch := newBatch(t, first, skipped)
	raw := batch.EncodedMessages()
	qualified := pgx.Identifier{"public", table}.Sanitize()
	function := pgx.Identifier{"public", table + "_skip"}.Sanitize()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if _, cleanupErr := pool.Exec(ctx, "DROP TABLE IF EXISTS "+qualified+"; DROP FUNCTION IF EXISTS "+function+"()"); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	statement := "CREATE FUNCTION " + function + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.messages = decode('" + hex.EncodeToString(raw) + "', 'hex') THEN RETURN NULL; END IF; RETURN NEW; END $$; " +
		"CREATE TRIGGER skip_message BEFORE INSERT ON " + qualified + " FOR EACH ROW EXECUTE FUNCTION " + function + "()"
	if _, execErr := pool.Exec(t.Context(), statement); execErr != nil {
		t.Fatal(execErr)
	}
	receipt, err := store.Append(t.Context(), batch)
	if err == nil || receipt != (history.Receipt{}) {
		t.Fatalf("missing acknowledgment accepted: receipt=%+v error=%v", receipt, err)
	}
	messages, err := store.Read(t.Context(), "conversation")
	if err != nil || len(messages) != 0 {
		t.Fatalf("failed batch left a prefix: messages=%v error=%v", messages, err)
	}
}

func TestNativeConstructionRejectsJSONB(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		t.Run(fmt.Sprint(initialize), func(t *testing.T) {
			pool := nativePool(t)
			table := nativeTable(t, pool)
			qualified := pgx.Identifier{"public", table}.Sanitize()
			if _, err := pool.Exec(t.Context(), "CREATE TABLE "+pgx.Identifier{"public", table + "_conversations"}.Sanitize()+" (conversation_id TEXT PRIMARY KEY, closed BOOLEAN NOT NULL); CREATE TABLE "+qualified+" (seq BIGSERIAL PRIMARY KEY, conversation_id TEXT NOT NULL, append_id TEXT NOT NULL, digest TEXT NOT NULL, messages JSONB)"); err != nil {
				t.Fatal(err)
			}
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, TableName: table, InitializeSchema: initialize})
			if err == nil || store != nil {
				t.Fatalf("JSONB schema accepted: store=%v error=%v", store, err)
			}
		})
	}
}

func TestNativeAppendRetriesAndClosureAcrossStoreInstances(t *testing.T) {
	pool := nativePool(t)
	table := nativeTable(t, pool)
	first, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, TableName: table, InitializeSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, TableName: table})
	if err != nil {
		t.Fatal(err)
	}
	batch := newBatch(t, chat.NewUserMessage(chat.NewTextPart("first")), chat.NewAssistantMessage(chat.NewTextPart("second")))
	var group sync.WaitGroup
	for _, store := range []*postgres.Store{first, second} {
		group.Go(func() {
			receipt, err := store.Append(t.Context(), batch)
			if err != nil || receipt.ValidateFor(batch) != nil {
				t.Errorf("concurrent append: receipt=%+v error=%v", receipt, err)
			}
		})
	}
	group.Wait()
	messages, err := first.Read(t.Context(), "conversation")
	if err != nil || !reflect.DeepEqual(messages, batch.Messages()) {
		t.Fatalf("dedup/order: messages=%v error=%v", messages, err)
	}
	changed := newBatch(t, chat.NewUserMessage(chat.NewTextPart("different")))
	if _, err := second.Append(t.Context(), changed); !errors.Is(err, history.ErrAppendConflict) {
		t.Fatalf("identity reused: %v", err)
	}
	if err := first.Clear(t.Context(), "conversation"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Append(t.Context(), batch); !errors.Is(err, history.ErrConversationClosed) {
		t.Fatalf("closed retry accepted: %v", err)
	}
	if messages, err := second.Read(t.Context(), "conversation"); err != nil || messages == nil || len(messages) != 0 {
		t.Fatalf("closed read: %v, %v", messages, err)
	}
	if ids, err := second.Conversations(t.Context()); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("closed listing: %v, %v", ids, err)
	}
	if err := first.Clear(t.Context(), "unknown"); err != nil {
		t.Fatal(err)
	}
	unknown, err := history.NewBatch("unknown", "append", chat.NewUserMessage(chat.NewTextPart("later")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Append(t.Context(), unknown); !errors.Is(err, history.ErrConversationClosed) {
		t.Fatalf("unknown identity reopened: %v", err)
	}
}
