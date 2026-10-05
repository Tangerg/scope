//go:build integration

package clickhouse_test

import (
	"context"
	"crypto/rand"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	storeclickhouse "github.com/Tangerg/scope/vectorstores/clickhouse"
)

type nativeFixture struct {
	conn   driver.Conn
	schema string
	model  embedding.Model
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	dsn := os.Getenv("SCOPE_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Fatal("SCOPE_CLICKHOUSE_DSN is required with -tags=integration; use an isolated ClickHouse server")
	}
	options, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	options.DialTimeout = 10 * time.Second
	options.ReadTimeout = 30 * time.Second
	conn, err := clickhouse.Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	f := &nativeFixture{conn: conn, schema: "scope_native_" + strings.ToLower(rand.Text())}
	if err := conn.Exec(t.Context(), "CREATE DATABASE "+f.schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if err := conn.Exec(ctx, "DROP DATABASE "+f.schema); err != nil {
			t.Errorf("drop isolated fixture: %v", err)
		}
	})
	f.model = embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for index, text := range request.Texts {
			vector := []float64{1, 0}
			if text == "far" {
				vector = []float64{0, 1}
			}
			outputs[index] = &embedding.Output{Embedding: vector}
		}
		return embedding.NewResponse(outputs, nil)
	})
	return f
}

func (n *nativeFixture) config(metric storeclickhouse.DistanceMetric, initialize bool) storeclickhouse.StoreConfig {
	return storeclickhouse.StoreConfig{
		Conn: n.conn, DatabaseName: n.schema, EmbeddingModel: n.model,
		DocumentBatcher: nativeBatcher{}, Dimensions: 2, DistanceMetric: metric, InitializeSchema: initialize,
	}
}

type nativeBatcher struct{}

func (n nativeBatcher) Batch(ctx context.Context, docs []*document.Document) ([][]*document.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return [][]*document.Document{docs}, nil
}

func TestNativeStoreSchemaAndDistances(t *testing.T) {
	for _, metric := range []storeclickhouse.DistanceMetric{storeclickhouse.DistanceCosine, storeclickhouse.DistanceL2} {
		t.Run(metric.String(), func(t *testing.T) {
			f := newNativeFixture(t)
			store, err := storeclickhouse.NewStore(t.Context(), f.config(metric, true))
			if err != nil {
				t.Fatalf("construct with native schema initialization: %v", err)
			}
			store, err = storeclickhouse.NewStore(t.Context(), f.config(metric, false))
			if err != nil {
				t.Fatalf("reopen initialized native schema: %v", err)
			}
			nearMetadata, err := metadata.FromValues(map[string]any{"name": "near", "count": 7, "active": true, "note": nil})
			if err != nil {
				t.Fatal(err)
			}
			farMetadata, err := metadata.FromValues(map[string]any{"name": "far"})
			if err != nil {
				t.Fatal(err)
			}
			docs := []*document.Document{
				{ID: "near", Text: "near", Metadata: nearMetadata},
				{ID: "far", Text: "far", Metadata: farMetadata},
			}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
				t.Fatal(err)
			}
			request := &vectorstore.SearchRequest{Query: "near", Options: vectorstore.SearchOptions{TopK: 2}}
			response, err := store.Search(t.Context(), request)
			if err != nil {
				t.Fatalf("read native distance and metadata: %v", err)
			}
			if len(response.Results) != 2 || response.Results[0].Document.ID != "near" || response.Results[1].Document.ID != "far" {
				t.Fatalf("ranked native results = %+v", response.Results)
			}
			if !response.Results[0].Document.Metadata.Equal(nearMetadata) || !response.Results[1].Document.Metadata.Equal(farMetadata) {
				t.Fatal("native result metadata differs from the indexed metadata")
			}
			wantFarScore := 0.5
			if metric == storeclickhouse.DistanceL2 {
				wantFarScore = 1 / (1 + math.Sqrt(2))
			}
			if response.Results[0].Score != 1 || math.Abs(response.Results[1].Score.Float64()-wantFarScore) > 1e-6 {
				t.Fatalf("native scores = %v, %v; want 1, %v", response.Results[0].Score, response.Results[1].Score, wantFarScore)
			}
			if err := store.DeleteIDs(t.Context(), []string{"near"}); err != nil {
				t.Fatal(err)
			}
			response, err = store.Search(t.Context(), request)
			if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "far" {
				t.Fatalf("native results after ID deletion = %+v, %v", response, err)
			}
			if err := store.DeleteWhere(t.Context(), filter.EQ("name", "far")); err != nil {
				t.Fatal(err)
			}
			response, err = store.Search(t.Context(), request)
			if err != nil || len(response.Results) != 0 {
				t.Fatalf("native results after filtered deletion = %+v, %v", response, err)
			}
		})
	}
}
