package oracle

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type offlineBatcher struct{}

func (o offlineBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{docs}, nil
}

func closedDatabase(t *testing.T) (*sql.DB, error) {
	t.Helper()
	connector := go_ora.NewConnector("oracle://unused:unused@invalid.invalid:1/unused")
	db := sql.OpenDB(connector)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	closedErr := db.PingContext(t.Context())
	if closedErr == nil {
		t.Fatal("closed database accepted a connection")
	}
	return db, closedErr
}

func offlineStore(t *testing.T, db *sql.DB, model embedding.Model) *Store {
	t.Helper()
	client, err := embeddingclient.New(model)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{db: db, fullTable: "documents", tableName: "documents", idColumn: "id", contentColumn: "content", metadataColumn: "facts", embeddingColumn: "embedding", embeddingClient: client, documentBatcher: offlineBatcher{}, distanceMetric: DistanceCosine}
}

func TestClosedDatabaseFailuresPreserveEffectsAndCause(t *testing.T) {
	db, closedErr := closedDatabase(t)
	for _, test := range []struct {
		source string
		calls  int
	}{
		{`value == 7`, 0}, {`value > 7`, 0}, {`value like 'a%'`, 0},
	} {
		t.Run(test.source, func(t *testing.T) {
			calls := 0
			store := offlineStore(t, db, embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
				calls++
				return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
			}))
			predicate, err := filter.Parse(test.source)
			if err != nil {
				t.Fatal(err)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
			if !errors.Is(err, closedErr) || response != nil || calls != test.calls {
				t.Fatalf("Search = %#v, %v; calls %d, want %d", response, err, calls, test.calls)
			}
			if err := store.DeleteWhere(t.Context(), predicate); !errors.Is(err, closedErr) {
				t.Fatalf("DeleteWhere = %v, want closed database", err)
			}
			if err := store.DeleteIDs(t.Context(), []string{"a", "A", "a "}); !errors.Is(err, closedErr) {
				t.Fatalf("DeleteIDs = %v, want closed database", err)
			}
		})
	}
}

func TestEmbeddingFailurePreventsQueriesAndWrites(t *testing.T) {
	failure := errors.New("embedding unavailable")
	store := offlineStore(t, nil, embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) { return nil, failure }))
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if !errors.Is(err, failure) || response != nil {
		t.Fatalf("Search = %#v, %v", response, err)
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "doc", Text: "text"}}}); !errors.Is(err, failure) {
		t.Fatalf("Index = %v", err)
	}
}

func TestUnfilteredSearchPropagatesQueryFailure(t *testing.T) {
	db, closedErr := closedDatabase(t)
	store := offlineStore(t, db, embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
	}))
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if !errors.Is(err, closedErr) || response != nil {
		t.Fatalf("Search = %#v, %v; want closed database", response, err)
	}
}

func TestIndexRejectsWholeInputBeforeEmbedding(t *testing.T) {
	calls := 0
	store := offlineStore(t, nil, embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		calls++
		return nil, errors.New("unexpected embedding")
	}))
	request := &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "valid", Text: "text"}, {ID: strings.Repeat("é", 1001), Text: "text"}}}
	if err := store.Index(t.Context(), request); !errors.Is(err, vectorstore.ErrInvalidDocument) || calls != 0 {
		t.Fatalf("Index = %v, embedding calls %d", err, calls)
	}
}

func TestIndexPropagatesPrepareFailure(t *testing.T) {
	db, closedErr := closedDatabase(t)
	store := offlineStore(t, db, embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
	}))
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "doc", Text: "text"}}}); !errors.Is(err, closedErr) {
		t.Fatalf("Index = %v, want closed database", err)
	}
}

func TestConstructionVerifiesSchemaBeforeEmbedding(t *testing.T) {
	db, closedErr := closedDatabase(t)
	calls := 0
	model := embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		calls++
		return nil, errors.New("unexpected embedding")
	})
	for _, initialize := range []bool{false, true} {
		config := StoreConfig{DB: db, EmbeddingModel: model, DocumentBatcher: offlineBatcher{}, Dimensions: 2, InitializeSchema: initialize}
		store, err := NewStore(t.Context(), config)
		if !errors.Is(err, closedErr) || store != nil || calls != 0 {
			t.Fatalf("NewStore initialize %t = %#v, %v; calls %d", initialize, store, err, calls)
		}
		config.SchemaName = "ISOLATED"
		if _, err := NewStore(t.Context(), config); !errors.Is(err, closedErr) {
			t.Fatalf("schema construction error = %v", err)
		}
	}
	if _, err := NewStore(t.Context(), StoreConfig{DB: db, EmbeddingModel: model, DocumentBatcher: offlineBatcher{}, InitializeSchema: true}); err == nil || !strings.Contains(err.Error(), "Dimensions must be > 0") {
		t.Fatalf("missing dimensions = %v", err)
	}
}
