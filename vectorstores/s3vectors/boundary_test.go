package s3vectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestConstructorBindsNativeSchemaAndValidatesEveryRecord(t *testing.T) {
	for _, metric := range []string{"cosine", "euclidean"} {
		t.Run(metric, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fixture.index["distanceMetric"] = metric
			store := fixture.store()
			if string(store.schema.metric) != metric || store.schema.dimensions != 2 {
				t.Fatal("native attributes were replaced by caller defaults")
			}
		})
	}
	for _, test := range []string{"name", "bucket", "dimension", "data type", "metric", "missing metadata configuration", "filterable content", "unknown nonfilterable key", "legacy record", "key projection", "vector width", "bad Core JSON"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t, &document.Document{ID: "first", Text: "text"}, &document.Document{ID: "late", Text: "text"})
			switch test {
			case "name":
				fixture.index["indexName"] = "other"
			case "bucket":
				fixture.index["vectorBucketName"] = "other"
			case "dimension":
				fixture.index["dimension"] = 0
			case "data type":
				fixture.index["dataType"] = "float64"
			case "metric":
				fixture.index["distanceMetric"] = "unknown"
			case "missing metadata configuration":
				delete(fixture.index, "metadataConfiguration")
			case "filterable content":
				fixture.index["metadataConfiguration"] = map[string]any{"nonFilterableMetadataKeys": []string{"scope_metadata"}}
			case "unknown nonfilterable key":
				fixture.index["metadataConfiguration"] = map[string]any{"nonFilterableMetadataKeys": []string{"scope_content", "scope_metadata", "extra"}}
			case "legacy record":
				fixture.records[1].Metadata = json.RawMessage(`{"scope_content":"text","tenant":"old"}`)
			case "key projection":
				fixture.records[1].Metadata = json.RawMessage(`{"scope_id":"other","scope_content":"text","scope_metadata":"{}"}`)
			case "vector width":
				fixture.records[1].Data.Vector = []float32{1}
			case "bad Core JSON":
				fixture.records[1].Metadata = json.RawMessage(`{"scope_id":"late","scope_content":"text","scope_metadata":"{]"}`)
			}
			calls := 0
			config := fixture.config()
			config.EmbeddingModel = embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
				calls++
				return constantEmbeddingModel().Call(ctx, request)
			})
			store, err := NewStore(t.Context(), config)
			if err == nil || store != nil || calls != 0 {
				t.Fatalf("unbound store = %v, %v, model calls=%d", store, err, calls)
			}
		})
	}
}

func TestSearchPreflightRunsBeforeModelAndNativeTopK(t *testing.T) {
	for _, test := range []string{"predicate", "malformed late record", "duplicate key", "invalid width", "list token loop"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t, &document.Document{ID: "one", Text: "text", Metadata: metadata.Map{"value": json.RawMessage(`42`)}})
			calls := 0
			config := fixture.config()
			config.EmbeddingModel = embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
				calls++
				return constantEmbeddingModel().Call(ctx, request)
			})
			store, err := NewStore(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			predicate := filter.IsNull("missing")
			if test == "predicate" {
				predicate = filter.Like("value", "%")
			}
			switch test {
			case "malformed late record":
				row := fixture.record(&document.Document{ID: "late", Text: "text"}, .5)
				row.Metadata = json.RawMessage(`{"scope_id":"late","scope_content":"text","scope_metadata":"{]"}`)
				fixture.records = append(fixture.records, row)
			case "duplicate key":
				fixture.records = append(fixture.records, fixture.records[0])
			case "invalid width":
				fixture.records[0].Data.Vector = []float32{1}
			case "list token loop":
				fixture.listScript = []string{`{"vectors":[],"nextToken":"one"}`, `{"vectors":[],"nextToken":"two"}`, `{"vectors":[],"nextToken":"one"}`}
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: predicate}})
			if err == nil || response != nil || calls != 0 || len(fixture.queries) != 0 {
				t.Fatalf("bad stored facts reached model or ANN: %v, %v", response, err)
			}
		})
	}
}

func TestNativeQueryPagesValidateToCompletionAndOwnRank(t *testing.T) {
	row := func(key string, distance string) string {
		return fmt.Sprintf(`{"key":%q,"distance":%s,"metadata":{"scope_id":%q,"scope_content":"text","scope_metadata":"{}"}}`, key, distance, key)
	}
	for _, test := range []string{"rank", "empty advancing page", "bad late metadata", "token loop", "metric changed", "nonfinite distance", "duplicate key", "outside membership"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t, &document.Document{ID: "a", Text: "text"}, &document.Document{ID: "b", Text: "text"})
			store := fixture.store()
			first := fmt.Sprintf(`{"distanceMetric":"cosine","vectors":[%s],"nextToken":"more"}`, row("a", "1"))
			second := fmt.Sprintf(`{"distanceMetric":"cosine","vectors":[%s]}`, row("b", "0"))
			predicate := filter.IsNull("missing")
			switch test {
			case "empty advancing page":
				first = `{"distanceMetric":"cosine","vectors":[],"nextToken":"more"}`
			case "bad late metadata":
				second = `{"distanceMetric":"cosine","vectors":[{"key":"b","distance":0,"metadata":{"scope_content":"old"}}]}`
			case "token loop":
				second = `{"distanceMetric":"cosine","vectors":[],"nextToken":"more"}`
			case "metric changed":
				second = fmt.Sprintf(`{"distanceMetric":"euclidean","vectors":[%s]}`, row("b", "0"))
			case "nonfinite distance":
				second = fmt.Sprintf(`{"distanceMetric":"cosine","vectors":[%s]}`, row("b", `"NaN"`))
			case "duplicate key":
				second = fmt.Sprintf(`{"distanceMetric":"cosine","vectors":[%s]}`, row("a", "0"))
			case "outside membership":
				second = fmt.Sprintf(`{"distanceMetric":"cosine","vectors":[%s]}`, row("other", "0"))
			}
			fixture.queryScript = []string{first, second}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, MinScore: 1, Filter: predicate}})
			if test == "rank" || test == "empty advancing page" {
				if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "b" || len(fixture.queries) != 2 {
					t.Fatalf("native rank or paging changed: %v, %v", response, err)
				}
			} else if err == nil || response != nil {
				t.Fatalf("bad late native result became success: %v, %v", response, err)
			}
		})
	}
}

func TestNativeResultCountAndKeyGroups(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.pageSize = 1000
	for i := range 1201 {
		fixture.records = append(fixture.records, fixture.record(&document.Document{ID: fmt.Sprintf("id-%04d", i), Text: "text"}, .5))
	}
	response, err := fixture.store().Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1201, Filter: filter.IsNull("missing")}})
	if err != nil || len(response.Results) != 1201 || len(fixture.queries) != 13 {
		t.Fatalf("native key selection groups = %v, %v, %d queries", response, err, len(fixture.queries))
	}
	for i, result := range response.Results {
		if result.Document.ID != fmt.Sprintf("id-%04d", i) {
			t.Fatal("native distance ties lost stable ID order")
		}
	}
}

func TestNativeWriteCountAndByteBudgets(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(strconv.FormatBool(large), func(t *testing.T) {
			fixture := newProtocolFixture(t)
			config := fixture.config()
			count, text := 501, "text"
			if large {
				count, text = 300, strings.Repeat("x", 35_000)
				fixture.index["dimension"] = 4096
				config.EmbeddingModel = embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
					outputs := make([]*embedding.Output, len(request.Texts))
					for i := range outputs {
						vector := make([]float64, 4096)
						for j := range vector {
							vector[j] = math.MaxFloat32
						}
						outputs[i] = &embedding.Output{Embedding: vector}
					}
					return embedding.NewResponse(outputs, nil)
				})
			}
			store, err := NewStore(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			docs := make([]*document.Document, count)
			for i := range docs {
				docs[i] = &document.Document{ID: strconv.Itoa(i), Text: text}
			}
			if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
				t.Fatal(err)
			}
			if len(fixture.putCounts) != 2 {
				t.Fatalf("native request budgets produced %v", fixture.putCounts)
			}
			for i, count := range fixture.putCounts {
				if count > MaxVectorsPerWrite || fixture.putBytes[i] > maxRequestBytes {
					t.Fatalf("native SDK payload exceeded a limit: %d records, %d bytes", count, fixture.putBytes[i])
				}
			}
		})
	}
}

func TestDeleteIDsPreflightsAndDeduplicatesTheWholeRequest(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = strconv.Itoa(i)
	}
	ids = append(ids, ids...)
	if err := store.DeleteIDs(t.Context(), ids); err != nil {
		t.Fatal(err)
	}
	if len(fixture.deleted) != 501 || !slices.Equal(fixture.deleted, ids[:501]) {
		t.Fatal("explicit IDs were not deduplicated in input order")
	}
	fixture.deleted = nil
	ids[len(ids)-1] = strings.Repeat("x", 1025)
	if err := store.DeleteIDs(t.Context(), ids); err == nil || len(fixture.deleted) != 0 {
		t.Fatal("invalid late ID caused partial deletion")
	}
}

func TestInvalidOptionsAndNativeFailuresReturnNoResponse(t *testing.T) {
	fixture := newProtocolFixture(t, &document.Document{ID: "one", Text: "text"})
	store := fixture.store()
	before := len(fixture.paths)
	for _, options := range []vectorstore.SearchOptions{{TopK: MaxTopK + 1}, {Mode: vectorstore.SearchModeHybrid}} {
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: options})
		if err == nil || response != nil || len(fixture.paths) != before {
			t.Fatal("invalid options reached native I/O")
		}
	}
	for _, path := range []string{"/ListVectors", "/QueryVectors"} {
		fixture.status[path] = 403
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
		if err == nil || response != nil {
			t.Fatal("native failure became success")
		}
		delete(fixture.status, path)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query"}); response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost its cause: %v, %v", response, err)
	}
}

func TestNativeMetricScoreProjection(t *testing.T) {
	for _, metric := range []types.DistanceMetric{types.DistanceMetricCosine, types.DistanceMetricEuclidean} {
		schema := indexSchema{dimensions: 2, metric: metric}
		if score, err := schema.score(0); err != nil || score != 1 {
			t.Fatal(score, err)
		}
		for _, distance := range []float64{math.NaN(), math.Inf(1), -1} {
			if _, err := schema.score(distance); err == nil {
				t.Fatal("invalid native distance was accepted")
			}
		}
	}
}
