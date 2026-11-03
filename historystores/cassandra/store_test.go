package cassandra_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/Tangerg/scope/historystores/cassandra"
)

type stubSession struct {
	*gocql.Session
	consistency gocql.Consistency
}

func (s *stubSession) Batch(kind gocql.BatchType) *gocql.Batch {
	return s.Session.Batch(kind).Consistency(s.consistency)
}

func TestNewStoreRequiresSession(t *testing.T) {
	for name, session := range map[string]cassandra.Session{
		"nil":       nil,
		"typed nil": (*gocql.Session)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			config := cassandra.StoreConfig{Session: session}
			if err := config.Validate(); err == nil {
				t.Fatal("StoreConfig.Validate should reject a nil Session")
			}
			_, err := cassandra.NewStore(t.Context(), config)
			if err == nil || !strings.Contains(err.Error(), "session") {
				t.Fatalf("NewStore() = %v; should reject the missing session", err)
			}
		})
	}
}

func TestNewStoreRejectsBadIdentifier(t *testing.T) {
	cases := []struct {
		name   string
		config cassandra.StoreConfig
	}{
		{"keyspace with hyphen", cassandra.StoreConfig{Session: &stubSession{Session: new(gocql.Session), consistency: gocql.Quorum}, Keyspace: "my-ks"}},
		{"table with semicolon", cassandra.StoreConfig{Session: &stubSession{Session: new(gocql.Session), consistency: gocql.Quorum}, TableName: "x;y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cassandra.NewStore(t.Context(), tc.config); err == nil {
				t.Fatal("expected identifier-validation error")
			}
		})
	}
}

func TestNewStoreAcceptsValidIdentifiers(t *testing.T) {
	_, err := cassandra.NewStore(t.Context(), cassandra.StoreConfig{
		Session:   &stubSession{Session: new(gocql.Session), consistency: gocql.Quorum},
		Keyspace:  "scope",
		TableName: "chat_history",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewStoreDoesNotDependOnSessionConsistency(t *testing.T) {
	for _, consistency := range []gocql.Consistency{gocql.Any, gocql.One, gocql.Quorum, gocql.LocalOne} {
		t.Run(consistency.String(), func(t *testing.T) {
			session := &stubSession{Session: new(gocql.Session), consistency: consistency}
			if _, err := cassandra.NewStore(t.Context(), cassandra.StoreConfig{Session: session}); err != nil {
				t.Fatal(err)
			}
			if session.consistency != consistency {
				t.Fatal("session defaults changed")
			}
		})
	}
}

func TestNewStoreHonorsCancellationBeforeIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store, err := cassandra.NewStore(ctx, cassandra.StoreConfig{Session: new(gocql.Session), InitializeSchema: true})
	if store != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("NewStore = %v, %v", store, err)
	}
}
