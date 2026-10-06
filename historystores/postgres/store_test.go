package postgres_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/historystores/postgres"
)

// Invalid configurations must fail before the pool is used.
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

func TestConfigAcceptsValidIdentifiers(t *testing.T) {
	err := (postgres.StoreConfig{
		Pool:       stubPool(),
		SchemaName: "my_schema",
		TableName:  "chat_history",
		IndexName:  "chat_history_lookup",
	}).Validate()
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestWriteConfirmsNoEffectWhenTransactionCannotBegin(t *testing.T) {
	pool := protocolPool(t, protocolStep{query: "SELECT message FROM public.chat_history LIMIT 0", replies: messageDescription()})
	store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}

	pool.Close()
	outcome, err := store.Write(t.Context(), history.ConversationID("conversation"), chat.NewUserMessage(chat.NewTextPart("hello")))
	if err == nil {
		t.Fatal("expected transaction admission error")
	}
	if outcome != (history.WriteOutcome{}) {
		t.Fatalf("outcome = %+v, want no effect before transaction admission", outcome)
	}
}
