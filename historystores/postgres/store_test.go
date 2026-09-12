package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/historystores/postgres"
)

// stubPool is a sentinel non-nil *pgxpool.Pool used to exercise the
// config-validation paths that don't actually issue SQL. The pool is
// never queried — tests that want real I/O need testcontainers or a
// live postgres and live outside the unit suite.
//
// pgxpool.Pool has unexported fields, so we can't construct one
// directly without a real connection. The cheap fix: tests that only
// inspect validation use a hand-built struct via pointer-to-zero.
func stubPool() *pgxpool.Pool { return new(pgxpool.Pool) }

func TestNewStoreRequiresPool(t *testing.T) {
	config := postgres.StoreConfig{}
	if err := config.Validate(); err == nil {
		t.Fatal("StoreConfig.Validate should reject a nil Pool")
	}
	_, err := postgres.NewStore(t.Context(), config)
	if err == nil {
		t.Fatal("expected error when Pool is nil")
	}
	if !strings.Contains(err.Error(), "pool") {
		t.Fatalf("err = %v; should mention pool", err)
	}
}

func TestNewStoreRejectsBadIdentifier(t *testing.T) {
	cases := []struct {
		name   string
		config postgres.StoreConfig
	}{
		{
			name:   "schema with semicolon",
			config: postgres.StoreConfig{Pool: stubPool(), SchemaName: "public; DROP TABLE x"},
		},
		{
			name:   "table with hyphen",
			config: postgres.StoreConfig{Pool: stubPool(), TableName: "chat history"},
		},
		{
			name:   "index starting with digit",
			config: postgres.StoreConfig{Pool: stubPool(), IndexName: "1bad"},
		},
		{
			name:   "table with space",
			config: postgres.StoreConfig{Pool: stubPool(), TableName: "chat history"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := postgres.NewStore(t.Context(), tc.config)
			if err == nil {
				t.Fatal("expected identifier-validation error")
			}
			if !strings.Contains(err.Error(), "valid unquoted identifier") {
				t.Fatalf("err = %v; should explain identifier requirement", err)
			}
		})
	}
}

func TestNewStoreAcceptsValidIdentifiers(t *testing.T) {
	// InitializeSchema=false so we don't issue SQL — only validation
	// runs. The stub pool would crash any real query.
	_, err := postgres.NewStore(t.Context(), postgres.StoreConfig{
		Pool:       stubPool(),
		SchemaName: "my_schema",
		TableName:  "chat_history",
		IndexName:  "chat_history_lookup",
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestWriteMarksOutcomeUncertainWhenBatchExecutionFails(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgres://127.0.0.1:1/db?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	config.MinConns = 0
	config.MaxConns = 1
	config.ConnConfig.ConnectTimeout = 10 * time.Millisecond

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store, err := postgres.NewStore(context.Background(), postgres.StoreConfig{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	outcome, err := store.Write(ctx, history.ConversationID("conversation"), chat.NewUserMessage(chat.NewTextPart("hello")))
	if err == nil {
		t.Fatal("expected batch execution error")
	}
	if outcome != (history.WriteOutcome{Uncertain: true}) {
		t.Fatalf("outcome = %+v, want uncertain outcome", outcome)
	}
}
