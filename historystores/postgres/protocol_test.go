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

func messageDescription() []pgproto3.BackendMessage {
	return []pgproto3.BackendMessage{
		&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("message"), DataTypeOID: pgtype.ByteaOID, DataTypeSize: -1, TypeModifier: -1}}},
		&pgproto3.CommandComplete{CommandTag: []byte("SELECT 0")},
	}
}

func TestNewStoreRejectsNonBinarySchema(t *testing.T) {
	for _, oid := range []uint32{pgtype.JSONBOID, pgtype.JSONOID, pgtype.TextOID} {
		t.Run(fmt.Sprint(oid), func(t *testing.T) {
			replies := messageDescription()
			replies[0].(*pgproto3.RowDescription).Fields[0].DataTypeOID = oid
			pool := protocolPool(t, protocolStep{query: "SELECT message FROM public.chat_history LIMIT 0", replies: replies})
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool})
			if store != nil || err == nil || !strings.Contains(err.Error(), "bytea") {
				t.Fatalf("non-binary schema accepted: store=%v error=%v", store, err)
			}
		})
	}
}

func TestNewStorePreservesSchemaQueryFailure(t *testing.T) {
	pool := protocolPool(t, protocolStep{query: "SELECT message FROM public.chat_history LIMIT 0", replies: []pgproto3.BackendMessage{&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42P01", Message: "table absent"}}})
	store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool})
	var native *pgconn.PgError
	if store != nil || !errors.As(err, &native) || native.Code != "42P01" {
		t.Fatalf("schema failure lost: store=%v error=%v", store, err)
	}
}

func TestNewStorePreservesSchemaCompletionFailure(t *testing.T) {
	replies := messageDescription()[:1]
	replies = append(replies, &pgproto3.ErrorResponse{Severity: "ERROR", Code: "42501", Message: "query failed after description"})
	pool := protocolPool(t, protocolStep{query: "SELECT message FROM public.chat_history LIMIT 0", replies: replies})
	store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool})
	var native *pgconn.PgError
	if store != nil || !errors.As(err, &native) || native.Code != "42501" {
		t.Fatalf("schema completion failure lost: store=%v error=%v", store, err)
	}
}

func commandReply(tag string) []pgproto3.BackendMessage {
	return []pgproto3.BackendMessage{&pgproto3.CommandComplete{CommandTag: []byte(tag)}}
}

func TestNewStoreInitializesBinarySchema(t *testing.T) {
	pool := protocolPool(t,
		protocolStep{query: "CREATE SCHEMA IF NOT EXISTS public", replies: commandReply("CREATE SCHEMA")},
		protocolStep{query: "CREATE TABLE IF NOT EXISTS public.chat_history ( seq BIGSERIAL PRIMARY KEY, conversation_id TEXT NOT NULL, message BYTEA NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now() )", replies: commandReply("CREATE TABLE")},
		protocolStep{query: "CREATE INDEX IF NOT EXISTS chat_history_conversation_idx ON public.chat_history (conversation_id, seq)", replies: commandReply("CREATE INDEX")},
		protocolStep{query: "SELECT message FROM public.chat_history LIMIT 0", replies: messageDescription()},
	)
	store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool, InitializeSchema: true})
	if err != nil || store == nil {
		t.Fatalf("binary initialization: store=%v error=%v", store, err)
	}
}

func TestWriteUsesNativeAcknowledgmentsAndCommitOutcome(t *testing.T) {
	message := chat.NewUserMessage(chat.NewTextPart("hello"))
	raw, err := message.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	insert := "INSERT INTO public.chat_history (conversation_id, message) VALUES ( 'conversation' , '\\x" + hex.EncodeToString(raw) + "' ::bytea)"
	for _, test := range []struct {
		name       string
		insert     []pgproto3.BackendMessage
		finalSQL   string
		final      []pgproto3.BackendMessage
		outcome    history.WriteOutcome
		invalid    bool
		rolledBack bool
	}{
		{name: "committed", insert: commandReply("INSERT 0 1"), finalSQL: "commit", final: commandReply("COMMIT"), outcome: history.WriteOutcome{Accepted: 1}},
		{name: "missing_row", insert: commandReply("INSERT 0 0"), finalSQL: "rollback", final: commandReply("ROLLBACK"), invalid: true},
		{name: "execution_failure", insert: []pgproto3.BackendMessage{&pgproto3.ErrorResponse{Severity: "ERROR", Code: "23514", Message: "constraint failed"}}, finalSQL: "rollback", final: commandReply("ROLLBACK"), invalid: true},
		{name: "commit_failure", insert: commandReply("INSERT 0 1"), finalSQL: "commit", final: []pgproto3.BackendMessage{&pgproto3.ErrorResponse{Severity: "ERROR", Code: "08007", Message: "commit result unknown"}}, outcome: history.WriteOutcome{Uncertain: true}, invalid: true},
		{name: "commit_rolled_back", insert: commandReply("INSERT 0 1"), finalSQL: "commit", final: commandReply("ROLLBACK"), invalid: true, rolledBack: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := protocolPool(t,
				protocolStep{query: "SELECT message FROM public.chat_history LIMIT 0", replies: messageDescription()},
				protocolStep{query: "begin", replies: commandReply("BEGIN"), status: 'T'},
				protocolStep{query: insert, replies: test.insert, status: 'T'},
				protocolStep{query: test.finalSQL, replies: test.final},
			)
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool})
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := store.Write(t.Context(), "conversation", message)
			if (err != nil) != test.invalid || outcome != test.outcome {
				t.Fatalf("native write settlement: outcome=%+v error=%v, want=%+v", outcome, err, test.outcome)
			}
			if test.rolledBack && !errors.Is(err, pgx.ErrTxCommitRollback) {
				t.Fatalf("native rollback classification lost: %v", err)
			}
			if outcomeErr := outcome.ValidateFor(1, err); outcomeErr != nil {
				t.Fatal(outcomeErr)
			}
		})
	}
}

func TestReadValidatesCanonicalWireAndNativeColumn(t *testing.T) {
	message := chat.NewUserMessage(chat.NewTextPart("hello\x00world"))
	raw, err := message.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		oid     uint32
		raw     []byte
		invalid bool
	}{
		{name: "canonical", oid: pgtype.ByteaOID, raw: raw},
		{name: "invalid_message", oid: pgtype.ByteaOID, raw: []byte(`{}`), invalid: true},
		{name: "replaced_column", oid: pgtype.JSONBOID, raw: raw, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			wireValue := []byte(`\x` + hex.EncodeToString(test.raw))
			if test.oid == pgtype.JSONBOID {
				wireValue = test.raw
			}
			pool := protocolPool(t,
				protocolStep{query: "SELECT message FROM public.chat_history LIMIT 0", replies: messageDescription()},
				protocolStep{query: "SELECT message FROM public.chat_history WHERE conversation_id = 'conversation' ORDER BY seq", replies: []pgproto3.BackendMessage{
					&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("message"), DataTypeOID: test.oid, DataTypeSize: -1, TypeModifier: -1}}},
					&pgproto3.DataRow{Values: [][]byte{wireValue}},
					&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")},
				}},
			)
			store, err := postgres.NewStore(t.Context(), postgres.StoreConfig{Pool: pool})
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.Read(t.Context(), "conversation")
			if test.invalid {
				if err == nil || got != nil {
					t.Fatalf("invalid native message accepted: messages=%v error=%v", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, []chat.Message{message}) {
				t.Fatalf("canonical native read: messages=%v error=%v", got, err)
			}
		})
	}
}
