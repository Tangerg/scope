package postgres_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/historystores/postgres"
)

type protocolStep struct {
	query   string
	replies []pgproto3.BackendMessage
	status  byte
}

// The real pgx pool consumes protocol replies. The fixture neither implements
// a production store interface nor encodes messages on behalf of Core.
type protocolPeer struct {
	steps []protocolStep
}

func (p protocolPeer) serve(conn net.Conn) error {
	defer conn.Close()
	backend := pgproto3.NewBackend(conn, conn)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return err
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := backend.Flush(); err != nil {
		return err
	}
	for _, step := range p.steps {
		message, err := backend.Receive()
		if err != nil {
			return err
		}
		query, ok := message.(*pgproto3.Query)
		if !ok || strings.Join(strings.Fields(query.String), " ") != step.query {
			return fmt.Errorf("query = %v, want %q", message, step.query)
		}
		for _, reply := range step.replies {
			backend.Send(reply)
		}
		status := step.status
		if status == 0 {
			status = 'I'
		}
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
		if err := backend.Flush(); err != nil {
			return err
		}
	}
	message, err := backend.Receive()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, ok := message.(*pgproto3.Terminate); !ok {
		return fmt.Errorf("unexpected protocol message: %T", message)
	}
	return nil
}

func protocolPool(t *testing.T, steps ...protocolStep) *pgxpool.Pool {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		done <- (protocolPeer{steps: steps}).serve(conn)
	}()
	config, err := pgxpool.ParseConfig("postgres://scope@" + listener.Addr().String() + "/scope?sslmode=disable")
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	config.MaxConns = 1
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("protocol peer did not stop")
		}
	})
	return pool
}

const schemaQuery = `SELECT a.messages FROM "public"."chat_history" AS a JOIN "public"."chat_history_conversations" AS c USING (conversation_id) WHERE a.append_id IS NOT NULL AND a.digest IS NOT NULL AND NOT c.closed LIMIT 0`

func messageDescription() []pgproto3.BackendMessage {
	return scalarRows("messages", pgtype.ByteaOID)
}

func scalarRows(name string, oid uint32, values ...string) []pgproto3.BackendMessage {
	replies := []pgproto3.BackendMessage{&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte(name), DataTypeOID: oid, DataTypeSize: -1, TypeModifier: -1}}}}
	for _, value := range values {
		replies = append(replies, &pgproto3.DataRow{Values: [][]byte{[]byte(value)}})
	}
	return append(replies, &pgproto3.CommandComplete{CommandTag: []byte(fmt.Sprintf("SELECT %d", len(values)))})
}

func commandReply(tag string) []pgproto3.BackendMessage {
	return []pgproto3.BackendMessage{&pgproto3.CommandComplete{CommandTag: []byte(tag)}}
}

func TestNewStoreRejectsNonBinarySchema(t *testing.T) {
	for _, oid := range []uint32{pgtype.JSONBOID, pgtype.JSONOID, pgtype.TextOID} {
		t.Run(fmt.Sprint(oid), func(t *testing.T) {
			pool := protocolPool(t, protocolStep{query: schemaQuery, replies: scalarRows("messages", oid)})
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool})
			if store != nil || err == nil || !strings.Contains(err.Error(), "bytea") {
				t.Fatalf("non-binary schema accepted: store=%v error=%v", store, err)
			}
		})
	}
}

func TestNewStorePreservesSchemaFailures(t *testing.T) {
	for _, afterDescription := range []bool{false, true} {
		t.Run(fmt.Sprint(afterDescription), func(t *testing.T) {
			replies := []pgproto3.BackendMessage{}
			if afterDescription {
				replies = append(replies, messageDescription()[0])
			}
			replies = append(replies, &pgproto3.ErrorResponse{Severity: "ERROR", Code: "42P01", Message: "table absent"})
			pool := protocolPool(t, protocolStep{query: schemaQuery, replies: replies})
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool})
			native, ok := errors.AsType[*pgconn.PgError](err)
			if store != nil || !ok || native.Code != "42P01" {
				t.Fatalf("schema failure lost: store=%v error=%v", store, err)
			}
		})
	}
}

func TestNewStoreInitializesBinarySchema(t *testing.T) {
	pool := protocolPool(t,
		protocolStep{query: `CREATE SCHEMA IF NOT EXISTS "public"`, replies: commandReply("CREATE SCHEMA")},
		protocolStep{query: `CREATE TABLE IF NOT EXISTS "public"."chat_history_conversations" (conversation_id TEXT PRIMARY KEY, closed BOOLEAN NOT NULL)`, replies: commandReply("CREATE TABLE")},
		protocolStep{query: `CREATE TABLE IF NOT EXISTS "public"."chat_history" (seq BIGSERIAL PRIMARY KEY, conversation_id TEXT NOT NULL REFERENCES "public"."chat_history_conversations" (conversation_id), append_id TEXT NOT NULL, digest TEXT NOT NULL, messages BYTEA, UNIQUE (conversation_id, append_id))`, replies: commandReply("CREATE TABLE")},
		protocolStep{query: `CREATE INDEX IF NOT EXISTS "chat_history_conversation_idx" ON "public"."chat_history" (conversation_id, seq)`, replies: commandReply("CREATE INDEX")},
		protocolStep{query: schemaQuery, replies: messageDescription()},
	)
	if store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, InitializeSchema: true}); err != nil || store == nil {
		t.Fatalf("initialization: store=%v error=%v", store, err)
	}
}

func newBatch(t *testing.T, messages ...chat.Message) history.Batch {
	t.Helper()
	batch, err := history.NewBatch("conversation", "append", messages...)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func lockedConversationSteps(closed bool) []protocolStep {
	value := "f"
	if closed {
		value = "t"
	}
	return []protocolStep{
		{query: "begin", replies: commandReply("BEGIN"), status: 'T'},
		{query: `INSERT INTO "public"."chat_history_conversations" (conversation_id, closed) VALUES ( 'conversation' , false) ON CONFLICT (conversation_id) DO NOTHING`, replies: commandReply("INSERT 0 0"), status: 'T'},
		{query: `SELECT closed FROM "public"."chat_history_conversations" WHERE conversation_id = 'conversation' FOR UPDATE`, replies: scalarRows("closed", pgtype.BoolOID, value), status: 'T'},
	}
}

func receiptStep(digests ...string) protocolStep {
	return protocolStep{query: `SELECT digest FROM "public"."chat_history" WHERE conversation_id = 'conversation' AND append_id = 'append'`, replies: scalarRows("digest", pgtype.TextOID, digests...), status: 'T'}
}

func insertQuery(batch history.Batch) string {
	return `INSERT INTO "public"."chat_history" (conversation_id, append_id, digest, messages) VALUES ( 'conversation' , 'append' , '` + batch.Digest() + `' , '\x` + hex.EncodeToString(batch.EncodedMessages()) + `' ::bytea)`
}

func TestAppendRequiresNativeAcknowledgment(t *testing.T) {
	batch := newBatch(t, chat.NewUserMessage(chat.NewTextPart("first")), chat.NewAssistantMessage(chat.NewTextPart("second")))
	for _, test := range []struct {
		name                string
		insert              []pgproto3.BackendMessage
		finalSQL            string
		final               []pgproto3.BackendMessage
		success, rolledBack bool
	}{
		{name: "committed", insert: commandReply("INSERT 0 1"), finalSQL: "commit", final: commandReply("COMMIT"), success: true},
		{name: "missing row", insert: commandReply("INSERT 0 0"), finalSQL: "rollback", final: commandReply("ROLLBACK")},
		{name: "execution failure", insert: []pgproto3.BackendMessage{&pgproto3.ErrorResponse{Severity: "ERROR", Code: "23514", Message: "constraint failed"}}, finalSQL: "rollback", final: commandReply("ROLLBACK")},
		{name: "commit acknowledgment loss", insert: commandReply("INSERT 0 1"), finalSQL: "commit", final: []pgproto3.BackendMessage{&pgproto3.ErrorResponse{Severity: "ERROR", Code: "08007", Message: "commit result unknown"}}},
		{name: "commit rollback", insert: commandReply("INSERT 0 1"), finalSQL: "commit", final: commandReply("ROLLBACK"), rolledBack: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			steps := []protocolStep{{query: schemaQuery, replies: messageDescription()}}
			steps = append(steps, lockedConversationSteps(false)...)
			steps = append(steps, receiptStep(), protocolStep{query: insertQuery(batch), replies: test.insert, status: 'T'}, protocolStep{query: test.finalSQL, replies: test.final})
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: protocolPool(t, steps...)})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := store.Append(t.Context(), batch)
			if test.success {
				if err != nil || receipt.ValidateFor(batch) != nil {
					t.Fatalf("receipt=%+v error=%v", receipt, err)
				}
			} else if err == nil || receipt != (history.Receipt{}) {
				t.Fatalf("unconfirmed append acknowledged: receipt=%+v error=%v", receipt, err)
			}
			if test.rolledBack && !errors.Is(err, pgx.ErrTxCommitRollback) {
				t.Fatalf("native rollback lost: %v", err)
			}
		})
	}
}

func TestAppendChecksClosureBeforeReceipt(t *testing.T) {
	batch := newBatch(t, chat.NewUserMessage(chat.NewTextPart("hello")))
	for _, test := range []struct {
		name   string
		closed bool
		digest string
		want   error
	}{
		{name: "duplicate", digest: batch.Digest()},
		{name: "conflict", digest: strings.Repeat("0", 64), want: history.ErrAppendConflict},
		{name: "closed", closed: true, want: history.ErrConversationClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			steps := []protocolStep{{query: schemaQuery, replies: messageDescription()}}
			steps = append(steps, lockedConversationSteps(test.closed)...)
			if !test.closed {
				steps = append(steps, receiptStep(test.digest))
			}
			steps = append(steps, protocolStep{query: "rollback", replies: commandReply("ROLLBACK")})
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: protocolPool(t, steps...)})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := store.Append(t.Context(), batch)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if test.want == nil {
				if receipt.ValidateFor(batch) != nil {
					t.Fatalf("receipt=%+v", receipt)
				}
			} else if receipt != (history.Receipt{}) {
				t.Fatalf("rejected append received receipt: %+v", receipt)
			}
		})
	}
}

func TestLostCommitAcknowledgmentReconcilesWithoutAnotherInsert(t *testing.T) {
	batch := newBatch(t, chat.NewUserMessage(chat.NewTextPart("hello")))
	steps := []protocolStep{{query: schemaQuery, replies: messageDescription()}}
	steps = append(steps, lockedConversationSteps(false)...)
	steps = append(steps, receiptStep(), protocolStep{query: insertQuery(batch), replies: commandReply("INSERT 0 1"), status: 'T'}, protocolStep{query: "commit", replies: []pgproto3.BackendMessage{&pgproto3.ErrorResponse{Severity: "ERROR", Code: "08007", Message: "acknowledgment lost"}}})
	steps = append(steps, lockedConversationSteps(false)...)
	steps = append(steps, receiptStep(batch.Digest()), protocolStep{query: "rollback", replies: commandReply("ROLLBACK")})
	store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: protocolPool(t, steps...)})
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := store.Append(t.Context(), batch); err == nil || receipt != (history.Receipt{}) {
		t.Fatalf("lost acknowledgment: %+v, %v", receipt, err)
	}
	if receipt, err := store.Append(t.Context(), batch); err != nil || receipt.ValidateFor(batch) != nil {
		t.Fatalf("reconciliation: %+v, %v", receipt, err)
	}
}

func TestClearCommitsClosureBeforeRemovingPayload(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprint(closed), func(t *testing.T) {
			steps := []protocolStep{{query: schemaQuery, replies: messageDescription()}}
			steps = append(steps, lockedConversationSteps(closed)...)
			steps = append(steps,
				protocolStep{query: `UPDATE "public"."chat_history_conversations" SET closed = true WHERE conversation_id = 'conversation'`, replies: commandReply("UPDATE 1"), status: 'T'},
				protocolStep{query: "commit", replies: commandReply("COMMIT")},
				protocolStep{query: `UPDATE "public"."chat_history" SET messages = NULL WHERE conversation_id = 'conversation'`, replies: commandReply("UPDATE 4")},
			)
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: protocolPool(t, steps...)})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Clear(t.Context(), "conversation"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadValidatesCanonicalWireAndNativeColumn(t *testing.T) {
	message := chat.NewUserMessage(chat.NewTextPart("hello\x00world"))
	batch := newBatch(t, message)
	for _, test := range []struct {
		name    string
		oid     uint32
		raw     []byte
		invalid bool
	}{
		{name: "canonical", oid: pgtype.ByteaOID, raw: batch.EncodedMessages()},
		{name: "invalid message", oid: pgtype.ByteaOID, raw: []byte(`[{}]`), invalid: true},
		{name: "empty append", oid: pgtype.ByteaOID, raw: []byte(`[]`), invalid: true},
		{name: "replaced column", oid: pgtype.JSONBOID, raw: batch.EncodedMessages(), invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := `\x` + hex.EncodeToString(test.raw)
			if test.oid == pgtype.JSONBOID {
				value = string(test.raw)
			}
			pool := protocolPool(t, protocolStep{query: schemaQuery, replies: messageDescription()}, protocolStep{query: `SELECT a.messages FROM "public"."chat_history" AS a JOIN "public"."chat_history_conversations" AS c USING (conversation_id) WHERE a.conversation_id = 'conversation' AND NOT c.closed ORDER BY a.seq`, replies: scalarRows("messages", test.oid, value)})
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool})
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.Read(t.Context(), "conversation")
			if test.invalid {
				if err == nil || got != nil {
					t.Fatalf("invalid payload accepted: messages=%v error=%v", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, []chat.Message{message}) {
				t.Fatalf("native read: messages=%v error=%v", got, err)
			}
		})
	}
}

func TestCleanupFailureDoesNotRollBackClosure(t *testing.T) {
	batch := newBatch(t, chat.NewUserMessage(chat.NewTextPart("retry")))
	steps := []protocolStep{{query: schemaQuery, replies: messageDescription()}}
	steps = append(steps, lockedConversationSteps(false)...)
	steps = append(steps,
		protocolStep{query: `UPDATE "public"."chat_history_conversations" SET closed = true WHERE conversation_id = 'conversation'`, replies: commandReply("UPDATE 1"), status: 'T'},
		protocolStep{query: "commit", replies: commandReply("COMMIT")},
		protocolStep{query: `UPDATE "public"."chat_history" SET messages = NULL WHERE conversation_id = 'conversation'`, replies: []pgproto3.BackendMessage{&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42501", Message: "cleanup failed"}}},
	)
	steps = append(steps, lockedConversationSteps(true)...)
	steps = append(steps, protocolStep{query: "rollback", replies: commandReply("ROLLBACK")})
	steps = append(steps, lockedConversationSteps(true)...)
	steps = append(steps,
		protocolStep{query: `UPDATE "public"."chat_history_conversations" SET closed = true WHERE conversation_id = 'conversation'`, replies: commandReply("UPDATE 1"), status: 'T'},
		protocolStep{query: "commit", replies: commandReply("COMMIT")},
		protocolStep{query: `UPDATE "public"."chat_history" SET messages = NULL WHERE conversation_id = 'conversation'`, replies: commandReply("UPDATE 1")},
	)
	store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: protocolPool(t, steps...)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(t.Context(), "conversation"); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if _, err := store.Append(t.Context(), batch); !errors.Is(err, history.ErrConversationClosed) {
		t.Fatalf("closure was lost: %v", err)
	}
	if err := store.Clear(t.Context(), "conversation"); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
}
