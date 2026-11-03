//go:build integration

package cassandra

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/history/storetest"
)

func TestNativeAtomicHistory(t *testing.T) {
	host, portText := os.Getenv("SCOPE_CASSANDRA_HOST"), os.Getenv("SCOPE_CASSANDRA_PORT")
	if host == "" || portText == "" {
		t.Fatal("SCOPE_CASSANDRA_HOST and SCOPE_CASSANDRA_PORT must identify an isolated test server")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cluster := gocql.NewCluster(host)
	cluster.Port = port
	cluster.DisableInitialHostLookup = true
	cluster.Timeout = 5 * time.Second
	cluster.PageSize = 2
	cluster.Consistency = gocql.One
	session, err := cluster.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	keyspace := "scope_history_" + strings.ToLower(rand.Text())
	if err := session.Query("CREATE KEYSPACE " + keyspace + " WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}").ExecContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := session.Query("DROP KEYSPACE " + keyspace).ExecContext(ctx); err != nil {
			t.Error(err)
		}
	})
	store, err := NewStore(t.Context(), StoreConfig{Session: session, Keyspace: keyspace, InitializeSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	const tableTTLSeconds = 3600
	qualified := keyspace + "." + DefaultTableName
	if err := session.Query(fmt.Sprintf("ALTER TABLE %s WITH default_time_to_live = %d", qualified, tableTTLSeconds)).ExecContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	storetest.RunAtomic(t, store)
	t.Run("permanent identity overrides table TTL", func(t *testing.T) {
		verifyPermanentIdentity(t, session, store, qualified)
	})
}

func verifyPermanentIdentity(t *testing.T, session *gocql.Session, store *Store, table string) {
	t.Helper()
	batch := batchFor(t, "permanent-append", "message")
	if _, err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	gateTTL := "SELECT TTL(closed), TTL(sequence) FROM " + table + " WHERE conversation_id = ? AND record_kind = ? AND record_id = ?"
	var closedTTL, sequenceTTL *int
	if err := session.Query(gateTTL, batch.ConversationID().String(), recordConversation, conversationRecordID).Consistency(gocql.Serial).ScanContext(t.Context(), &closedTTL, &sequenceTTL); err != nil {
		t.Fatal(err)
	}
	if closedTTL != nil || sequenceTTL != nil {
		t.Fatalf("open gate inherits TTL: closed=%v sequence=%v", closedTTL, sequenceTTL)
	}
	var digestTTL, messagesTTL *int
	appendTTL := "SELECT TTL(sequence), TTL(digest), TTL(messages) FROM " + table + " WHERE conversation_id = ? AND record_kind = ? AND record_id = ?"
	if err := session.Query(appendTTL, batch.ConversationID().String(), recordAppend, batch.ID().String()).Consistency(gocql.Serial).ScanContext(t.Context(), &sequenceTTL, &digestTTL, &messagesTTL); err != nil {
		t.Fatal(err)
	}
	if sequenceTTL != nil || digestTTL != nil || messagesTTL != nil {
		t.Fatalf("append inherits TTL: sequence=%v digest=%v messages=%v", sequenceTTL, digestTTL, messagesTTL)
	}
	for _, id := range []history.ConversationID{batch.ConversationID(), "never-written"} {
		if err := store.Clear(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if err := session.Query(gateTTL, id.String(), recordConversation, conversationRecordID).Consistency(gocql.Serial).ScanContext(t.Context(), &closedTTL, &sequenceTTL); err != nil {
			t.Fatal(err)
		}
		if closedTTL != nil || sequenceTTL != nil {
			t.Fatalf("closed gate inherits TTL: closed=%v sequence=%v", closedTTL, sequenceTTL)
		}
	}
}
