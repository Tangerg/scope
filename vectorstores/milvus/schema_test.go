package milvus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/milvus-io/milvus-proto/go-api/v2/commonpb"
	"github.com/milvus-io/milvus-proto/go-api/v2/milvuspb"
	"github.com/milvus-io/milvus-proto/go-api/v2/schemapb"
	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

// schemaService exercises the official client's collection, index, and search
// decoding through a local gRPC connection. It never contacts a Milvus server.
type schemaService struct {
	milvuspb.UnimplementedMilvusServiceServer
	mu          sync.Mutex
	schema      *schemapb.CollectionSchema
	indexes     []*milvuspb.IndexDescription
	missing     bool
	created     int
	indexed     int
	loaded      int
	searched    int
	described   int
	indexReads  int
	searchScore []float32
}

func newSchemaService(metric entity.MetricType) *schemaService {
	return &schemaService{
		schema: entity.NewSchema().WithName("documents").
			WithField(entity.NewField().WithName("id").WithDataType(entity.FieldTypeVarChar).WithMaxLength(36).WithIsPrimaryKey(true)).
			WithField(entity.NewField().WithName("vector").WithDataType(entity.FieldTypeFloatVector).WithDim(2)).
			WithField(entity.NewField().WithName("content").WithDataType(entity.FieldTypeVarChar).WithMaxLength(65535)).
			WithField(entity.NewField().WithName("metadata").WithDataType(entity.FieldTypeJSON)).ProtoMessage(),
		indexes: []*milvuspb.IndexDescription{{
			FieldName: "vector", IndexName: "vectors", State: commonpb.IndexState_Finished,
			Params: []*commonpb.KeyValuePair{{Key: "metric_type", Value: string(metric)}},
		}},
	}
}

func (s *schemaService) Connect(context.Context, *milvuspb.ConnectRequest) (*milvuspb.ConnectResponse, error) {
	return &milvuspb.ConnectResponse{Status: &commonpb.Status{}, Identifier: 1}, nil
}

func (s *schemaService) DescribeCollection(context.Context, *milvuspb.DescribeCollectionRequest) (*milvuspb.DescribeCollectionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.described++
	if s.missing {
		return &milvuspb.DescribeCollectionResponse{Status: &commonpb.Status{Code: 100, Reason: "collection not found"}}, nil
	}
	return &milvuspb.DescribeCollectionResponse{Status: &commonpb.Status{}, CollectionID: 1, Schema: s.schema}, nil
}

func (s *schemaService) CreateCollection(ctx context.Context, request *milvuspb.CreateCollectionRequest) (*commonpb.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var schema schemapb.CollectionSchema
	if err := proto.Unmarshal(request.Schema, &schema); err != nil {
		return nil, err
	}
	s.schema, s.missing = &schema, false
	s.created++
	return &commonpb.Status{}, nil
}

func (s *schemaService) DescribeIndex(ctx context.Context, request *milvuspb.DescribeIndexRequest) (*milvuspb.DescribeIndexResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexReads++
	if len(s.indexes) == 0 {
		return &milvuspb.DescribeIndexResponse{Status: &commonpb.Status{Code: 700, Reason: "index not found"}}, nil
	}
	var descriptions []*milvuspb.IndexDescription
	for _, description := range s.indexes {
		if request.IndexName == "" || request.IndexName == description.IndexName {
			descriptions = append(descriptions, description)
		}
	}
	return &milvuspb.DescribeIndexResponse{Status: &commonpb.Status{}, IndexDescriptions: descriptions}, nil
}

func (s *schemaService) CreateIndex(ctx context.Context, request *milvuspb.CreateIndexRequest) (*commonpb.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexed++
	s.indexes = []*milvuspb.IndexDescription{{
		FieldName: request.FieldName, IndexName: "vectors", Params: request.ExtraParams, State: commonpb.IndexState_Finished,
	}}
	return &commonpb.Status{}, nil
}

func (s *schemaService) LoadCollection(context.Context, *milvuspb.LoadCollectionRequest) (*commonpb.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded++
	return &commonpb.Status{}, nil
}

func (s *schemaService) GetLoadingProgress(context.Context, *milvuspb.GetLoadingProgressRequest) (*milvuspb.GetLoadingProgressResponse, error) {
	return &milvuspb.GetLoadingProgressResponse{Status: &commonpb.Status{}, Progress: 100}, nil
}

func (s *schemaService) Search(context.Context, *milvuspb.SearchRequest) (*milvuspb.SearchResults, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searched++
	return &milvuspb.SearchResults{Status: &commonpb.Status{}, Results: &schemapb.SearchResultData{
		NumQueries: 1, TopK: 2, Topks: []int64{2}, Scores: s.searchScore,
		Ids: &schemapb.IDs{IdField: &schemapb.IDs_StrId{StrId: &schemapb.StringArray{Data: []string{"first", "second"}}}},
		FieldsData: []*schemapb.FieldData{
			column.NewColumnVarChar("id", []string{"first", "second"}).FieldData(),
			column.NewColumnVarChar("content", []string{"first result", "second result"}).FieldData(),
			column.NewColumnJSONBytes("metadata", [][]byte{[]byte(`{}`), []byte(`{}`)}).FieldData(),
		},
	}}, nil
}

func newCollectionClient(t *testing.T, service milvuspb.MilvusServiceServer) *milvusclient.Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	milvuspb.RegisterMilvusServiceServer(server, service)
	served := make(chan error, 1)
	go func() {
		served <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		if err := listener.Close(); err != nil {
			t.Errorf("close local Milvus listener: %v", err)
		}
		if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("serve local Milvus protocol: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address: "bufnet",
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(context.Background()); err != nil {
			t.Errorf("close local Milvus client: %v", err)
		}
	})
	return client
}

func schemaConfig(client *milvusclient.Client, metric entity.MetricType, initialize bool, calls *atomic.Int64) StoreConfig {
	return StoreConfig{
		Client: client, CollectionName: "documents", InitializeSchema: initialize,
		MetricType: metric, DocumentBatcher: upsertBatcher{},
		EmbeddingModel: embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
			calls.Add(1)
			outputs := make([]*embedding.Output, len(request.Texts))
			for i := range outputs {
				outputs[i] = &embedding.Output{Embedding: []float64{2, 0}}
			}
			return embedding.NewResponse(outputs, nil)
		}),
	}
}

func TestNewStoreVerifiesExistingIndexMetricInBothModes(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		for _, actual := range []entity.MetricType{entity.COSINE, entity.IP, entity.L2} {
			for _, configured := range []entity.MetricType{entity.COSINE, entity.IP, entity.L2} {
				t.Run(fmt.Sprintf("initialize_%t/%s/want_%s", initialize, actual, configured), func(t *testing.T) {
					service := newSchemaService(actual)
					var embeddings atomic.Int64
					store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), configured, initialize, &embeddings))
					if actual != configured {
						if !errors.Is(err, ErrSchemaMismatch) || store != nil {
							t.Fatalf("NewStore = %v, %v; want metric mismatch", store, err)
						}
					} else if err != nil || store.dimensions != 2 {
						t.Fatalf("NewStore = %v, %v; want discovered dimension 2", store, err)
					}
					service.mu.Lock()
					defer service.mu.Unlock()
					wantLoads := 0
					if initialize && actual == configured {
						wantLoads = 1
					}
					if service.created != 0 || service.indexed != 0 || service.loaded != wantLoads || embeddings.Load() != 0 {
						t.Fatalf("unexpected writes/load/embeddings = %d/%d/%d/%d", service.created, service.indexed, service.loaded, embeddings.Load())
					}
					if service.described == 0 || service.indexReads == 0 {
						t.Fatal("existing collection was accepted without describing schema and index")
					}
				})
			}
		}
	}
}

func TestNewStoreRejectsIncompatibleExistingSchema(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*schemaService)
	}{
		{"automatic IDs", func(s *schemaService) { s.schema.Fields[0].AutoID = true }},
		{"missing metadata", func(s *schemaService) { s.schema.Fields = s.schema.Fields[:3] }},
		{"duplicate field", func(s *schemaService) { s.schema.Fields = append(s.schema.Fields, s.schema.Fields[0]) }},
		{"integer IDs", func(s *schemaService) { s.schema.Fields[0].DataType = schemapb.DataType_Int64 }},
		{"ID is not primary", func(s *schemaService) { s.schema.Fields[0].IsPrimaryKey = false }},
		{"short IDs", func(s *schemaService) { s.schema.Fields[0].TypeParams[0].Value = "8" }},
		{"wrong vector type", func(s *schemaService) { s.schema.Fields[1].DataType = schemapb.DataType_BinaryVector }},
		{"zero vector dimension", func(s *schemaService) { s.schema.Fields[1].TypeParams[0].Value = "0" }},
		{"invalid vector dimension", func(s *schemaService) { s.schema.Fields[1].TypeParams[0].Value = "bad" }},
		{"missing vector dimension", func(s *schemaService) { s.schema.Fields[1].TypeParams = nil }},
		{"short content", func(s *schemaService) { s.schema.Fields[2].TypeParams[0].Value = "8" }},
		{"nullable content", func(s *schemaService) { s.schema.Fields[2].Nullable = true }},
		{"metadata is text", func(s *schemaService) { s.schema.Fields[3].DataType = schemapb.DataType_VarChar }},
		{"required extra field", func(s *schemaService) {
			s.schema.Fields = append(s.schema.Fields, &schemapb.FieldSchema{Name: "tenant", DataType: schemapb.DataType_Int64})
		}},
		{"metric is absent", func(s *schemaService) { s.indexes[0].Params = nil }},
		{"ambiguous vector indexes", func(s *schemaService) {
			s.indexes = append(s.indexes, proto.Clone(s.indexes[0]).(*milvuspb.IndexDescription))
		}},
	}
	for _, initialize := range []bool{false, true} {
		for _, sample := range cases {
			t.Run(fmt.Sprintf("initialize_%t/%s", initialize, sample.name), func(t *testing.T) {
				service := newSchemaService(entity.COSINE)
				sample.mutate(service)
				var embeddings atomic.Int64
				store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, initialize, &embeddings))
				if !errors.Is(err, ErrSchemaMismatch) || store != nil {
					t.Fatalf("NewStore = %v, %v; want schema mismatch", store, err)
				}
				service.mu.Lock()
				defer service.mu.Unlock()
				if service.created+service.indexed+service.loaded != 0 || embeddings.Load() != 0 {
					t.Fatal("incompatible collection triggered writes, loading, or embedding")
				}
			})
		}
	}
}

func TestNewStoreDiscoversOrVerifiesDimensions(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		for _, dimensions := range []int{0, 2, 3} {
			t.Run(fmt.Sprintf("initialize_%t/dimensions_%d", initialize, dimensions), func(t *testing.T) {
				service := newSchemaService(entity.COSINE)
				var embeddings atomic.Int64
				config := schemaConfig(newCollectionClient(t, service), entity.COSINE, initialize, &embeddings)
				config.Dimensions = dimensions
				store, err := NewStore(t.Context(), config)
				if dimensions == 3 {
					if !errors.Is(err, ErrSchemaMismatch) {
						t.Fatalf("dimension mismatch = %v", err)
					}
					return
				}
				if err != nil || store.dimensions != 2 || embeddings.Load() != 0 {
					t.Fatalf("NewStore = %v, %v; embedding calls = %d", store, err, embeddings.Load())
				}
			})
		}
	}
}

func TestNewStoreCreatesOnlyMissingResources(t *testing.T) {
	for _, sample := range []struct {
		name       string
		missing    bool
		initialize bool
		dimensions int
		wantError  bool
		created    int
		indexed    int
	}{
		{name: "missing collection read only", missing: true, wantError: true},
		{name: "missing index read only", wantError: true},
		{name: "creation needs dimension", missing: true, initialize: true, wantError: true},
		{name: "create collection and index", missing: true, initialize: true, dimensions: 2, created: 1, indexed: 1},
		{name: "create only index", initialize: true, indexed: 1},
	} {
		t.Run(sample.name, func(t *testing.T) {
			service := newSchemaService(entity.COSINE)
			service.missing, service.indexes = sample.missing, nil
			var embeddings atomic.Int64
			config := schemaConfig(newCollectionClient(t, service), entity.COSINE, sample.initialize, &embeddings)
			config.Dimensions = sample.dimensions
			store, err := NewStore(t.Context(), config)
			if (err != nil) != sample.wantError {
				t.Fatalf("NewStore = %v, %v", store, err)
			}
			service.mu.Lock()
			defer service.mu.Unlock()
			if service.created != sample.created || service.indexed != sample.indexed || embeddings.Load() != 0 {
				t.Fatalf("created/indexed/embeddings = %d/%d/%d", service.created, service.indexed, embeddings.Load())
			}
		})
	}
}

func TestVerifiedMetricControlsSearchScoresAndMinimum(t *testing.T) {
	// The query embedding is [2,0]. These scores correspond to non-unit stored
	// vectors: IP [4,0]/[1,0], L2 [2,0]/[4,0], COSINE [4,0]/[0,4].
	for _, sample := range []struct {
		metric entity.MetricType
		raw    []float32
		min    vectorstore.Score
		want   float64
	}{
		{entity.IP, []float32{8, 2}, 0.9, 1 / (1 + math.Exp(-8))},
		{entity.L2, []float32{0, 4}, 0.5, 1},
		{entity.COSINE, []float32{1, 0}, 0.75, 1},
	} {
		t.Run(string(sample.metric), func(t *testing.T) {
			service := newSchemaService(sample.metric)
			service.searchScore = sample.raw
			var embeddings atomic.Int64
			store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), sample.metric, false, &embeddings))
			if err != nil {
				t.Fatal(err)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 2, MinScore: sample.min}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Document.ID != "first" || math.Abs(float64(response.Results[0].Score)-sample.want) > 1e-12 {
				t.Fatalf("results = %#v; want only first with score %.12f", response.Results, sample.want)
			}
		})
	}
}

func TestDiscoveredDimensionRejectsIncompatibleEmbeddingsBeforeMilvusIO(t *testing.T) {
	service := newSchemaService(entity.COSINE)
	service.schema.Fields[1].TypeParams[0].Value = "3"
	var embeddings atomic.Int64
	store, err := NewStore(t.Context(), schemaConfig(newCollectionClient(t, service), entity.COSINE, false, &embeddings))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if err == nil || !strings.Contains(err.Error(), "dimension 2, want 3") {
		t.Fatalf("Search = %v", err)
	}
	err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "document"}}})
	if err == nil || !strings.Contains(err.Error(), "dimension 2, want 3") {
		t.Fatalf("Index = %v", err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.searched != 0 {
		t.Fatalf("search RPC count = %d", service.searched)
	}
}
