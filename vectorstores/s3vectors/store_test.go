package s3vectors

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3vectors"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type protocolRecord struct {
	Key  string `json:"key"`
	Data struct {
		Vector []float32 `json:"float32"`
	} `json:"data"`
	Metadata json.RawMessage `json:"metadata"`
	Distance float32         `json:"distance"`
}

type protocolFixture struct {
	t           *testing.T
	mu          sync.Mutex
	server      *httptest.Server
	client      *awss3.Client
	index       map[string]any
	records     []protocolRecord
	paths       []string
	queries     []json.RawMessage
	putCounts   []int
	putBytes    []int
	deleted     []string
	pageSize    int
	listScript  []string
	queryScript []string
	status      map[string]int
}

func newProtocolFixture(t *testing.T, docs ...*document.Document) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{t: t, pageSize: 2, status: make(map[string]int), index: map[string]any{
		"indexName": "documents", "vectorBucketName": "bucket", "dimension": 2, "dataType": "float32", "distanceMetric": "cosine",
		"metadataConfiguration": map[string]any{"nonFilterableMetadataKeys": []string{"scope_content", "scope_metadata"}},
	}}
	for _, doc := range docs {
		fixture.records = append(fixture.records, fixture.record(doc, .5))
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	fixture.client = awss3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: aws.AnonymousCredentials{}}, func(options *awss3.Options) {
		options.BaseEndpoint = aws.String(fixture.server.URL)
		options.HTTPClient = fixture.server.Client()
		options.RetryMaxAttempts = 1
	})
	return fixture
}

func (p *protocolFixture) record(doc *document.Document, distance float32) protocolRecord {
	p.t.Helper()
	facts, err := doc.Metadata.MarshalJSON()
	if err != nil {
		p.t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(map[string]string{"scope_id": doc.ID, "scope_content": doc.Text, "scope_metadata": string(facts)})
	if err != nil {
		p.t.Fatal(err)
	}
	row := protocolRecord{Key: doc.ID, Metadata: encoded, Distance: distance}
	row.Data.Vector = []float32{1, 0}
	return row
}

func (p *protocolFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		p.t.Error(err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	p.paths = append(p.paths, request.URL.Path)
	writer.Header().Set("Content-Type", "application/json")
	if status := p.status[request.URL.Path]; status != 0 {
		writer.WriteHeader(status)
		fmt.Fprint(writer, `{"message":"native failure"}`)
		return
	}
	var input struct {
		Bucket         string           `json:"vectorBucketName"`
		Index          string           `json:"indexName"`
		Next           string           `json:"nextToken"`
		ReturnMetadata bool             `json:"returnMetadata"`
		ReturnData     bool             `json:"returnData"`
		ReturnDistance bool             `json:"returnDistance"`
		TopK           int              `json:"topK"`
		Filter         json.RawMessage  `json:"filter"`
		Vectors        []protocolRecord `json:"vectors"`
		Keys           []string         `json:"keys"`
	}
	if err = jsonv2.Unmarshal(body, &input); err != nil {
		p.t.Error(err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if request.Method != http.MethodPost || input.Bucket != "bucket" || input.Index != "documents" {
		p.t.Error("unexpected native target")
	}
	var output any
	switch request.URL.Path {
	case "/GetIndex":
		output = map[string]any{"index": p.index}
	case "/ListVectors":
		if !input.ReturnMetadata || !input.ReturnData {
			p.t.Error("complete native record preflight was not requested")
		}
		if len(p.listScript) != 0 {
			fmt.Fprint(writer, p.listScript[0])
			p.listScript = p.listScript[1:]
			return
		}
		offset := 0
		if input.Next != "" {
			offset, err = strconv.Atoi(input.Next)
			if err != nil {
				p.t.Error(err)
				return
			}
		}
		end := min(offset+p.pageSize, len(p.records))
		rows := make([]map[string]any, 0, end-offset)
		for _, row := range p.records[offset:end] {
			rows = append(rows, map[string]any{"key": row.Key, "metadata": row.Metadata, "data": row.Data})
		}
		page := map[string]any{"vectors": rows}
		if end < len(p.records) {
			page["nextToken"] = strconv.Itoa(end)
		}
		output = page
	case "/PutVectors":
		p.putCounts = append(p.putCounts, len(input.Vectors))
		p.putBytes = append(p.putBytes, len(body))
		for _, row := range input.Vectors {
			index := slices.IndexFunc(p.records, func(existing protocolRecord) bool { return existing.Key == row.Key })
			row.Distance = .5
			if index < 0 {
				p.records = append(p.records, row)
			} else {
				p.records[index] = row
			}
		}
		return
	case "/QueryVectors":
		p.queries = append(p.queries, slices.Clone(body))
		if !input.ReturnMetadata || !input.ReturnDistance {
			p.t.Error("native metadata or distance was not requested")
		}
		if len(p.queryScript) != 0 {
			fmt.Fprint(writer, p.queryScript[0])
			p.queryScript = p.queryScript[1:]
			return
		}
		var keys []string
		if len(input.Filter) != 0 {
			var selection struct {
				ID struct {
					Keys []string `json:"$in"`
				} `json:"scope_id"`
			}
			if err = jsonv2.Unmarshal(input.Filter, &selection, jsonv2.RejectUnknownMembers(true)); err != nil {
				p.t.Error(err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			keys = selection.ID.Keys
			if len(keys) == 0 {
				p.t.Error("empty native ID selection")
			}
		}
		rows := make([]protocolRecord, 0, len(p.records))
		for _, row := range p.records {
			if keys == nil || slices.Contains(keys, row.Key) {
				rows = append(rows, row)
			}
		}
		slices.SortFunc(rows, func(left, right protocolRecord) int {
			if left.Distance < right.Distance {
				return -1
			}
			if left.Distance > right.Distance {
				return 1
			}
			return strings.Compare(left.Key, right.Key)
		})
		rows = rows[:min(len(rows), input.TopK)]
		offset := 0
		if input.Next != "" {
			offset, err = strconv.Atoi(input.Next)
			if err != nil {
				p.t.Error(err)
				return
			}
		}
		end := min(offset+min(p.pageSize, MaxResultsPerQueryPage), len(rows))
		hits := make([]map[string]any, 0, end-offset)
		for _, row := range rows[offset:end] {
			hits = append(hits, map[string]any{"key": row.Key, "metadata": row.Metadata, "distance": row.Distance})
		}
		page := map[string]any{"vectors": hits, "distanceMetric": p.index["distanceMetric"]}
		if end < len(rows) {
			page["nextToken"] = strconv.Itoa(end)
		}
		output = page
	case "/DeleteVectors":
		p.deleted = append(p.deleted, input.Keys...)
		return
	default:
		p.t.Errorf("unexpected native operation %s", request.URL.Path)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	encoded, err := jsonv2.Marshal(output)
	if err != nil {
		p.t.Error(err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	writer.Write(encoded)
}

func (p *protocolFixture) config() StoreConfig {
	return StoreConfig{Client: p.client, VectorBucketName: "bucket", IndexName: "documents", EmbeddingModel: constantEmbeddingModel(), DocumentBatcher: testBatcher{size: 1000}}
}

func (p *protocolFixture) store() *Store {
	p.t.Helper()
	store, err := NewStore(p.t.Context(), p.config())
	if err != nil {
		p.t.Fatal(err)
	}
	return store
}

type testBatcher struct{ size int }

func (t testBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, t.size)), nil
}

func constantEmbeddingModel() embedding.Model {
	return embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
}

func TestFilterConformanceAtNativeSDKBoundary(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		fixture := newProtocolFixture(t, docs...)
		response, err := fixture.store().Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, result := range response.Results {
			ids = append(ids, result.Document.ID)
		}
		return ids, nil
	}})
}

func TestIndexRoundTripsOnlyCoreMetadataAndReplacesRecords(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	sources := []metadata.Map{nil, {}, {"n": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"n":1.0000000000000000001,"huge":1e1000}`), "scope_content": json.RawMessage(`"user fact"`)}}
	var docs []*document.Document
	for i, facts := range sources {
		docs = append(docs, &document.Document{ID: strconv.Itoa(i), Text: "text", Metadata: facts})
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 3}})
	if err != nil {
		t.Fatal(err)
	}
	for i, result := range response.Results {
		if !sources[i].Equal(result.Document.Metadata) || (sources[i] == nil) != (result.Document.Metadata == nil) {
			t.Fatalf("metadata %d changed: %v", i, result.Document.Metadata)
		}
	}
	if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "2", Text: "replacement", Metadata: metadata.Map{}}}}); err != nil {
		t.Fatal(err)
	}
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 3}})
	if err != nil || response.Results[2].Document.Text != "replacement" || response.Results[2].Document.Metadata == nil || len(response.Results[2].Document.Metadata) != 0 {
		t.Fatalf("replacement retained old facts: %v, %v", response, err)
	}
}

func TestIndexPreflightsAllBatchesBeforePublishing(t *testing.T) {
	for _, test := range []string{"embedding", "dimension", "overflow", "underflow", "metadata", "media"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			config := fixture.config()
			config.DocumentBatcher = testBatcher{size: 1}
			failure := errors.New("later embedding failure")
			calls := 0
			config.EmbeddingModel = embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
				calls++
				if calls == 2 {
					switch test {
					case "embedding":
						return nil, failure
					case "dimension":
						return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1}}}, nil)
					case "overflow":
						return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{math.MaxFloat64, 0}}}, nil)
					case "underflow":
						return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1e-100, 0}}}, nil)
					}
				}
				return constantEmbeddingModel().Call(ctx, request)
			})
			store, err := NewStore(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			docs := []*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}}
			if test == "metadata" {
				docs[1].Text = strings.Repeat("x", maxMetadataBytes)
			}
			if test == "media" {
				docs[1].Media = &media.Media{MIME: "image/png", Source: media.Source{Kind: media.SourceBytes, Bytes: []byte{1}}}
			}
			err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs})
			if err == nil || len(fixture.putCounts) != 0 || (test == "embedding" && !errors.Is(err, failure)) {
				t.Fatalf("partial publication: %v, %v", fixture.putCounts, err)
			}
			if (test == "metadata" || test == "media") && calls != 0 {
				t.Fatal("invalid document reached embedding")
			}
		})
	}
}
