//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
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
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+pgx.Identifier{"public", table}.Sanitize()); err != nil {
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
			outcome, err := store.Write(t.Context(), history.ConversationID("conversation"), message)
			if err != nil || outcome != (history.WriteOutcome{Accepted: 1}) {
				t.Fatalf("canonical write: outcome=%+v error=%v", outcome, err)
			}
			messages, err := store.Read(t.Context(), history.ConversationID("conversation"))
			if err != nil || !reflect.DeepEqual(messages, []chat.Message{message}) {
				t.Fatalf("canonical round trip: messages=%+v error=%v, want=%+v", messages, err, message)
			}
		})
	}
}

func TestNativeWriteRequiresEveryInsertAcknowledgment(t *testing.T) {
	pool := nativePool(t)
	table := nativeTable(t, pool)
	store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, TableName: table, InitializeSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	first := chat.NewUserMessage(chat.NewTextPart("first"))
	skipped := chat.NewUserMessage(chat.NewTextPart("skipped"))
	raw, err := skipped.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	qualified := pgx.Identifier{"public", table}.Sanitize()
	function := pgx.Identifier{"public", table + "_skip"}.Sanitize()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if _, cleanupErr := pool.Exec(ctx, "DROP TABLE IF EXISTS "+qualified+"; DROP FUNCTION IF EXISTS "+function+"()"); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	statement := "CREATE FUNCTION " + function + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.message = decode('" + hex.EncodeToString(raw) + "', 'hex') THEN RETURN NULL; END IF; RETURN NEW; END $$; " +
		"CREATE TRIGGER skip_message BEFORE INSERT ON " + qualified + " FOR EACH ROW EXECUTE FUNCTION " + function + "()"
	if _, execErr := pool.Exec(t.Context(), statement); execErr != nil {
		t.Fatal(execErr)
	}
	outcome, err := store.Write(t.Context(), "conversation", first, skipped)
	if err == nil || outcome != (history.WriteOutcome{}) {
		t.Fatalf("missing acknowledgment accepted: outcome=%+v error=%v", outcome, err)
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
			if _, err := pool.Exec(t.Context(), "CREATE TABLE "+qualified+" (seq BIGSERIAL PRIMARY KEY, conversation_id TEXT NOT NULL, message JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now())"); err != nil {
				t.Fatal(err)
			}
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, TableName: table, InitializeSchema: initialize})
			if err == nil || store != nil {
				t.Fatalf("JSONB schema accepted: store=%v error=%v", store, err)
			}
		})
	}
}

func TestNativeWriteValidatesWholeBatchAndPreservesOrder(t *testing.T) {
	pool := nativePool(t)
	store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, TableName: nativeTable(t, pool), InitializeSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	first := chat.NewUserMessage(chat.NewTextPart("first"))
	second := chat.NewAssistantMessage(chat.NewTextPart("second"))
	if outcome, err := store.Write(t.Context(), "conversation", first, chat.Message{}); err == nil || outcome != (history.WriteOutcome{}) {
		t.Fatalf("invalid batch accepted: outcome=%+v error=%v", outcome, err)
	}
	if messages, err := store.Read(t.Context(), "conversation"); err != nil || len(messages) != 0 {
		t.Fatalf("invalid batch wrote a prefix: messages=%v error=%v", messages, err)
	}
	if outcome, err := store.Write(t.Context(), "conversation", first, second); err != nil || outcome != (history.WriteOutcome{Accepted: 2}) {
		t.Fatalf("valid batch: outcome=%+v error=%v", outcome, err)
	}
	if messages, err := store.Read(t.Context(), "conversation"); err != nil || !reflect.DeepEqual(messages, []chat.Message{first, second}) {
		t.Fatalf("argument order lost: messages=%v error=%v", messages, err)
	}
	if ids, err := store.Conversations(t.Context()); err != nil || !reflect.DeepEqual(ids, []history.ConversationID{"conversation"}) {
		t.Fatalf("conversation listing: ids=%v error=%v", ids, err)
	}
	if err := store.Clear(t.Context(), "conversation"); err != nil {
		t.Fatal(err)
	}
	if messages, err := store.Read(t.Context(), "conversation"); err != nil || len(messages) != 0 {
		t.Fatalf("clear did not remove conversation: messages=%v error=%v", messages, err)
	}
}
