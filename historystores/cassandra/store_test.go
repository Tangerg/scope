package cassandra_test

import (
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

// Cassandra accepts a write at ANY when "a single replica may respond, or the
// coordinator may store a hint", and replays the hint later. A hint is not
// readable and is lost if it expires before delivery, so a store that accepted
// that level would return nil from Write for a message no replica holds.
//
// ANY is also the zero value of gocql.Consistency, so this catches a session
// whose consistency was never configured at all.
func TestNewStoreRefusesConsistencyThatMayOnlyStoreAHint(t *testing.T) {
	for name, session := range map[string]cassandra.Session{
		"explicit ANY":     &stubSession{Session: new(gocql.Session), consistency: gocql.Any},
		"never configured": new(gocql.Session),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := cassandra.NewStore(t.Context(), cassandra.StoreConfig{
				Session: session, Keyspace: "scope", TableName: "chat_history",
			})
			if !errors.Is(err, cassandra.ErrUnacknowledgedWrites) {
				t.Fatalf("NewStore() = %v, want ErrUnacknowledgedWrites", err)
			}
		})
	}
}

// Every other level means a successful write reached at least one replica, so
// none of them is refused.
func TestNewStoreAcceptsEveryDurableConsistency(t *testing.T) {
	for _, consistency := range []gocql.Consistency{
		gocql.One, gocql.Two, gocql.Three, gocql.Quorum,
		gocql.All, gocql.LocalQuorum, gocql.EachQuorum, gocql.LocalOne,
	} {
		t.Run(consistency.String(), func(t *testing.T) {
			session := &stubSession{Session: new(gocql.Session), consistency: consistency}
			if _, err := cassandra.NewStore(t.Context(), cassandra.StoreConfig{
				Session: session, Keyspace: "scope", TableName: "chat_history",
			}); err != nil {
				t.Fatalf("NewStore() = %v, want nil", err)
			}
		})
	}
}
