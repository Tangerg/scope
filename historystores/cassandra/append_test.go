package cassandra

import (
	"reflect"
	"strings"
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

func batchFor(t *testing.T, id, text string) history.Batch {
	t.Helper()
	batch, err := history.NewBatch("conversation", history.AppendID(id), chat.NewUserMessage(chat.NewTextPart(text)))
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func TestConditionalAppendSharesGatePartitionAndCanonicalPayload(t *testing.T) {
	store, err := NewStore(t.Context(), StoreConfig{Session: new(gocql.Session)})
	if err != nil {
		t.Fatal(err)
	}
	batch := batchFor(t, "stable", "message")
	transaction := store.conditionalAppend(batch, 4)
	if transaction.Type != gocql.LoggedBatch || transaction.GetConsistency() != gocql.Quorum {
		t.Fatalf("transaction boundary = %+v", transaction)
	}
	if consistency, configured := transaction.GetSerialConsistency(); !configured || consistency != gocql.Serial {
		t.Fatal("transaction lacks SERIAL Paxos boundary")
	}
	if len(transaction.Entries) != 2 {
		t.Fatalf("entries = %v", transaction.Entries)
	}
	gate, appendEntry := transaction.Entries[0], transaction.Entries[1]
	if !strings.Contains(gate.Stmt, "IF closed = false AND sequence = ?") || !strings.HasSuffix(appendEntry.Stmt, "IF NOT EXISTS USING TTL 0") {
		t.Fatalf("missing conditional boundary: %v", transaction.Entries)
	}
	if !reflect.DeepEqual(gate.Args, []any{int64(5), batch.ConversationID().String(), recordConversation, conversationRecordID, int64(4)}) {
		t.Fatalf("gate arguments = %v", gate.Args)
	}
	if !reflect.DeepEqual(appendEntry.Args, []any{batch.ConversationID().String(), recordAppend, batch.ID().String(), int64(5), batch.Digest(), string(batch.EncodedMessages())}) {
		t.Fatalf("append arguments = %v", appendEntry.Args)
	}
	if !strings.Contains(store.createCQL, "PRIMARY KEY ((conversation_id), record_kind, record_id)") || strings.Contains(store.clearCQL, "record_id") {
		t.Fatalf("schema or cleanup boundary changed: %s / %s", store.createCQL, store.clearCQL)
	}
}

func TestIdentityWritesOverrideTableTTL(t *testing.T) {
	store, err := NewStore(t.Context(), StoreConfig{Session: new(gocql.Session)})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{store.initializeCQL, store.appendCQL} {
		if !strings.HasSuffix(statement, "IF NOT EXISTS USING TTL 0") {
			t.Fatalf("insert inherits the table TTL: %s", statement)
		}
	}
	for _, statement := range []string{store.advanceCQL, store.closeCQL} {
		if !strings.Contains(statement, " USING TTL 0 SET ") {
			t.Fatalf("update inherits the table TTL: %s", statement)
		}
	}
}

func TestReadDecodesOnlyTheCapturedSequence(t *testing.T) {
	first := batchFor(t, "first", "one")
	second := batchFor(t, "second", "two")
	messages, err := decodeRecords([]appendRecord{{sequence: 2, messages: string(second.EncodedMessages())}, {sequence: 3, messages: "not in snapshot"}, {sequence: 1, messages: string(first.EncodedMessages())}}, 2)
	if err != nil || len(messages) != 2 || messages[0].Text() != "one" || messages[1].Text() != "two" {
		t.Fatalf("snapshot = %v, %v", messages, err)
	}
	messages[0] = chat.NewUserMessage(chat.NewTextPart("changed"))
	unchanged, err := decodeRecords([]appendRecord{{sequence: 1, messages: string(first.EncodedMessages())}}, 1)
	if err != nil || unchanged[0].Text() != "one" {
		t.Fatal("decoded snapshot aliases stored data")
	}
}

func TestReadRejectsCorruptAppendRows(t *testing.T) {
	encoded := string(batchFor(t, "batch", "one").EncodedMessages())
	for name, records := range map[string][]appendRecord{
		"zero sequence":      {{sequence: 0, messages: encoded}},
		"duplicate sequence": {{sequence: 1, messages: encoded}, {sequence: 1, messages: encoded}},
		"invalid messages":   {{sequence: 1, messages: "{"}},
		"empty batch":        {{sequence: 1, messages: "[]"}},
	} {
		t.Run(name, func(t *testing.T) {
			if messages, err := decodeRecords(records, 2); err == nil || messages != nil {
				t.Fatalf("decode = %v, %v", messages, err)
			}
		})
	}
	if messages, err := decodeRecords(nil, 0); err != nil || messages == nil || len(messages) != 0 {
		t.Fatalf("empty snapshot = %v, %v", messages, err)
	}
}

func TestCleanupUsesTheCommittedClosureTimestamp(t *testing.T) {
	store, err := NewStore(t.Context(), StoreConfig{Session: new(gocql.Session)})
	if err != nil {
		t.Fatal(err)
	}
	for _, gate := range []conversationGate{{}, {closed: true}, {closed: false, closedWriteTimestamp: 100}} {
		if query, cleanupErr := store.cleanupQuery("conversation", gate); cleanupErr == nil || query != nil {
			t.Fatalf("invalid closure produced cleanup: %v, %v", query, cleanupErr)
		}
	}
	query, err := store.cleanupQuery("conversation", conversationGate{closed: true, closedWriteTimestamp: 100})
	if err != nil {
		t.Fatal(err)
	}
	if query.GetConsistency() != gocql.Quorum || !strings.Contains(query.Statement(), "USING TIMESTAMP ?") || !reflect.DeepEqual(query.Values(), []any{int64(100), "conversation", recordAppend}) {
		t.Fatalf("cleanup used a new wallclock or touched the permanent gate: %v", query)
	}
}
