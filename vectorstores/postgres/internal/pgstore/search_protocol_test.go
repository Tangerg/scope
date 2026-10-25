package pgstore_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/postgres"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore"
)

type searchPeer struct {
	queries int
}

func (s *searchPeer) serve(conn net.Conn) error {
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
	status := byte('I')
	for {
		message, err := backend.Receive()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := message.(*pgproto3.Terminate); ok {
			return nil
		}
		query, ok := message.(*pgproto3.Query)
		if !ok {
			return fmt.Errorf("unexpected protocol message %T", message)
		}
		tag := "SELECT 1"
		switch {
		case strings.Contains(query.String, "FROM pg_attribute"):
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{
				{Name: []byte("attname"), DataTypeOID: pgtype.TextOID}, {Name: []byte("typname"), DataTypeOID: pgtype.TextOID},
				{Name: []byte("atttypmod"), DataTypeOID: pgtype.Int4OID}, {Name: []byte("attnotnull"), DataTypeOID: pgtype.BoolOID},
				{Name: []byte("atthasdef"), DataTypeOID: pgtype.BoolOID}, {Name: []byte("attnum"), DataTypeOID: pgtype.Int2OID},
			}})
			for _, row := range [][]string{{"id", "bytea", "-1", "t", "f", "1"}, {"content", "text", "-1", "t", "f", "2"}, {"facts", "bytea", "-1", "t", "f", "3"}, {"embedding", "vector", "2", "t", "f", "4"}} {
				values := make([][]byte, len(row))
				for i, value := range row {
					values[i] = []byte(value)
				}
				backend.Send(&pgproto3.DataRow{Values: values})
			}
			tag = "SELECT 4"
		case strings.Contains(query.String, "FROM pg_constraint"):
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("conkey"), DataTypeOID: pgtype.Int2ArrayOID}}})
			backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("{1}")}})
		case query.String == "begin isolation level serializable read only":
			tag, status = "BEGIN", 'T'
		case query.String == "commit":
			tag, status = "COMMIT", 'I'
		case query.String == "rollback":
			tag, status = "ROLLBACK", 'I'
		case strings.HasPrefix(query.String, "SELECT id,content,"):
			// A native ranking query needs the vector, limit, and distance order.
			// A preliminary full-source read cannot satisfy this boundary.
			statement := strings.Join(strings.Fields(query.String), " ")
			if strings.Contains(statement, "WHERE") || !strings.Contains(statement, "embedding <=> '[1,0]' AS distance") || !strings.Contains(statement, "ORDER BY distance,id LIMIT '1'") {
				return fmt.Errorf("unfiltered search did not issue one bounded native ranking query: %s", query.String)
			}
			s.queries++
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{
				{Name: []byte("id"), DataTypeOID: pgtype.ByteaOID}, {Name: []byte("content"), DataTypeOID: pgtype.TextOID},
				{Name: []byte("facts"), DataTypeOID: pgtype.ByteaOID}, {Name: []byte("embedding"), DataTypeOID: pgtype.TextOID},
				{Name: []byte("distance"), DataTypeOID: pgtype.Float8OID},
			}})
			backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(`\x6f6e65`), []byte("text"), []byte(`\x6e756c6c`), []byte("[1,0]"), []byte("0")}})
		default:
			return fmt.Errorf("unexpected query: %s", query.String)
		}
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
		if err = backend.Flush(); err != nil {
			return err
		}
	}
}

func TestUnfilteredSearchUsesNativeRankingWithoutSourceEnumeration(t *testing.T) {
	client, server := net.Pipe()
	peer := &searchPeer{}
	done := make(chan error, 1)
	go func() { done <- peer.serve(server) }()
	config, err := pgxpool.ParseConfig("postgres://scope@localhost/scope?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		client.Close()
		server.Close()
		if peerErr := <-done; peerErr != nil {
			t.Error(peerErr)
		}
		if peer.queries != 1 {
			t.Errorf("ranking queries = %d", peer.queries)
		}
	})
	model := embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
	})
	store, err := pgstore.New(t.Context(), pgstore.Config{Provider: "pgvector", Pool: pool, SchemaName: "public", TableName: "documents", MetadataColumn: "facts", EmbeddingModel: model, DocumentBatcher: singleDocumentBatcher{}, DistanceMetric: postgres.DistanceCosine})
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" || response.Results[0].Score != 1 {
		t.Fatalf("native query = %v, %v", response, err)
	}
}
