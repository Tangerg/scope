package milvus

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"

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

type fixtureRecord struct {
	id       string
	text     string
	metadata string
	vector   []float32
}

type nativeService struct {
	milvuspb.UnimplementedMilvusServiceServer
	mu           sync.Mutex
	schema       *schemapb.CollectionSchema
	metric       entity.MetricType
	records      []fixtureRecord
	writes       int
	queries      int
	searches     int
	deletions    int
	groups       []int
	queryHook    func(*milvuspb.QueryResults)
	searchHook   func(*milvuspb.SearchResults)
	upsertHook   func(*milvuspb.MutationResult)
	beforeDelete func()
	beforeSearch func()
	nativeError  error
}

func nativeSchema(name string) *entity.Schema {
	return entity.NewSchema().WithName(name).
		WithField(entity.NewField().WithName(fieldID).WithDataType(entity.FieldTypeVarChar).WithMaxLength(nativeMaxVarCharBytes).WithIsPrimaryKey(true)).
		WithField(entity.NewField().WithName(fieldContent).WithDataType(entity.FieldTypeVarChar).WithMaxLength(nativeMaxVarCharBytes)).
		WithField(entity.NewField().WithName(fieldMeta).WithDataType(entity.FieldTypeVarChar).WithMaxLength(nativeMaxVarCharBytes)).
		WithField(entity.NewField().WithName(fieldVector).WithDataType(entity.FieldTypeFloatVector).WithDim(2))
}

func newNativeService(metric entity.MetricType) *nativeService {
	return &nativeService{schema: nativeSchema("documents").ProtoMessage(), metric: metric}
}

func (n *nativeService) Connect(context.Context, *milvuspb.ConnectRequest) (*milvuspb.ConnectResponse, error) {
	return &milvuspb.ConnectResponse{Status: &commonpb.Status{}, Identifier: 1}, nil
}

func (n *nativeService) DescribeCollection(context.Context, *milvuspb.DescribeCollectionRequest) (*milvuspb.DescribeCollectionResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &milvuspb.DescribeCollectionResponse{Status: &commonpb.Status{}, CollectionID: 1, Schema: proto.Clone(n.schema).(*schemapb.CollectionSchema)}, n.nativeError
}

func (n *nativeService) DescribeIndex(_ context.Context, request *milvuspb.DescribeIndexRequest) (*milvuspb.DescribeIndexResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &milvuspb.DescribeIndexResponse{Status: &commonpb.Status{}, IndexDescriptions: []*milvuspb.IndexDescription{{FieldName: fieldVector, IndexName: "vectors", State: commonpb.IndexState_Finished, Params: []*commonpb.KeyValuePair{{Key: "metric_type", Value: string(n.metric)}}}}}, n.nativeError
}

func fixtureFields(records []fixtureRecord) []*schemapb.FieldData {
	ids, texts, facts := make([]string, len(records)), make([]string, len(records)), make([]string, len(records))
	vectors := make([][]float32, len(records))
	for position, record := range records {
		ids[position], texts[position], facts[position], vectors[position] = record.id, record.text, record.metadata, record.vector
	}
	return []*schemapb.FieldData{column.NewColumnVarChar(fieldID, ids).FieldData(), column.NewColumnVarChar(fieldContent, texts).FieldData(), column.NewColumnVarChar(fieldMeta, facts).FieldData(), column.NewColumnFloatVector(fieldVector, 2, vectors).FieldData()}
}

func (n *nativeService) Query(_ context.Context, request *milvuspb.QueryRequest) (*milvuspb.QueryResults, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.queries++
	if request.ConsistencyLevel != commonpb.ConsistencyLevel_Strong || request.UseDefaultConsistency {
		return nil, errors.New("fixture: source read must be strong")
	}
	after := request.ExprTemplateValues["after"].GetStringVal()
	var records []fixtureRecord
	for _, record := range n.records {
		if record.id > after {
			records = append(records, record)
		}
	}
	slices.SortFunc(records, func(left, right fixtureRecord) int { return cmp.Compare(left.id, right.id) })
	limit := sourcePageSize
	for _, param := range request.QueryParams {
		if param.Key == "limit" {
			value, err := strconv.Atoi(param.Value)
			if err != nil {
				return nil, err
			}
			limit = value
		}
	}
	records = records[:min(limit, len(records))]
	response := &milvuspb.QueryResults{Status: &commonpb.Status{}, OutputFields: request.OutputFields, FieldsData: fixtureFields(records)}
	if n.queryHook != nil {
		n.queryHook(response)
	}
	return response, n.nativeError
}

func (n *nativeService) Upsert(_ context.Context, request *milvuspb.UpsertRequest) (*milvuspb.MutationResult, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.nativeError != nil {
		return nil, n.nativeError
	}
	fields := make(map[string]*schemapb.FieldData)
	for _, field := range request.FieldsData {
		fields[field.FieldName] = field
	}
	ids := fields[fieldID].GetScalars().GetStringData().GetData()
	texts, facts := fields[fieldContent].GetScalars().GetStringData().GetData(), fields[fieldMeta].GetScalars().GetStringData().GetData()
	dense := fields[fieldVector].GetVectors().GetFloatVector().GetData()
	if len(fields) != 4 || len(ids) != int(request.NumRows) || len(texts) != len(ids) || len(facts) != len(ids) || len(dense) != len(ids)*2 {
		return nil, errors.New("fixture: inconsistent current write")
	}
	for position, id := range ids {
		n.records = slices.DeleteFunc(n.records, func(record fixtureRecord) bool { return record.id == id })
		n.records = append(n.records, fixtureRecord{id: id, text: texts[position], metadata: facts[position], vector: slices.Clone(dense[position*2 : position*2+2])})
	}
	n.writes++
	response := &milvuspb.MutationResult{Status: &commonpb.Status{}, UpsertCnt: int64(request.NumRows), IDs: &schemapb.IDs{IdField: &schemapb.IDs_StrId{StrId: &schemapb.StringArray{Data: slices.Clone(ids)}}}}
	if n.upsertHook != nil {
		n.upsertHook(response)
	}
	return response, nil
}

func (n *nativeService) Search(_ context.Context, request *milvuspb.SearchRequest) (*milvuspb.SearchResults, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.searches++
	if n.beforeSearch != nil {
		n.beforeSearch()
	}
	if request.Dsl != "id in {ids}" || request.ConsistencyLevel != commonpb.ConsistencyLevel_Strong {
		return nil, errors.New("fixture: query must use native identities and strong consistency")
	}
	ids := request.ExprTemplateValues["ids"].GetArrayVal().GetStringData().GetData()
	n.groups = append(n.groups, len(ids))
	var placeholder commonpb.PlaceholderGroup
	if err := proto.Unmarshal(request.GetPlaceholderGroup(), &placeholder); err != nil {
		return nil, err
	}
	raw := placeholder.Placeholders[0].Values[0]
	query := []float32{math.Float32frombits(binary.LittleEndian.Uint32(raw[:4])), math.Float32frombits(binary.LittleEndian.Uint32(raw[4:8]))}
	limit := 0
	for _, param := range request.SearchParams {
		if param.Key == "topk" {
			value, err := strconv.Atoi(param.Value)
			if err != nil {
				return nil, err
			}
			limit = value
		}
		if param.Key == "metric_type" && param.Value != string(n.metric) {
			return nil, errors.New("fixture: native metric was overridden")
		}
	}
	type scored struct {
		record fixtureRecord
		score  float32
	}
	var ranked []scored
	for _, record := range n.records {
		if !slices.Contains(ids, record.id) {
			continue
		}
		score := record.vector[0]*query[0] + record.vector[1]*query[1]
		if n.metric == entity.L2 {
			x, y := record.vector[0]-query[0], record.vector[1]-query[1]
			score = x*x + y*y
		}
		if n.metric == entity.COSINE {
			score /= float32(math.Hypot(float64(record.vector[0]), float64(record.vector[1])) * math.Hypot(float64(query[0]), float64(query[1])))
		}
		ranked = append(ranked, scored{record: record, score: score})
	}
	slices.SortFunc(ranked, func(left, right scored) int {
		order := cmp.Compare(right.score, left.score)
		if n.metric == entity.L2 {
			order = -order
		}
		if order != 0 {
			return order
		}
		return cmp.Compare(left.record.id, right.record.id)
	})
	ranked = ranked[:min(limit, len(ranked))]
	records, scores, selected := make([]fixtureRecord, len(ranked)), make([]float32, len(ranked)), make([]string, len(ranked))
	for position, value := range ranked {
		records[position], scores[position], selected[position] = value.record, value.score, value.record.id
	}
	response := &milvuspb.SearchResults{Status: &commonpb.Status{}, Results: &schemapb.SearchResultData{NumQueries: 1, TopK: int64(limit), Topks: []int64{int64(len(records))}, Scores: scores, Ids: &schemapb.IDs{IdField: &schemapb.IDs_StrId{StrId: &schemapb.StringArray{Data: selected}}}, FieldsData: fixtureFields(records)}}
	if n.searchHook != nil {
		n.searchHook(response)
	}
	return response, n.nativeError
}

func (n *nativeService) Delete(_ context.Context, request *milvuspb.DeleteRequest) (*milvuspb.MutationResult, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.deletions++
	if n.nativeError != nil {
		return nil, n.nativeError
	}
	if n.beforeDelete != nil {
		n.beforeDelete()
	}
	if request.ConsistencyLevel != commonpb.ConsistencyLevel_Strong {
		return nil, errors.New("fixture: delete must be strong")
	}
	ids := request.ExprTemplateValues["ids"].GetArrayVal().GetStringData().GetData()
	guard, guarded := request.ExprTemplateValues["metadata"]
	if guarded && request.Expr != "id in {ids} and metadata == {metadata}" {
		return nil, errors.New("fixture: invalid metadata guard")
	}
	if !guarded && request.Expr != "id in {ids}" {
		return nil, errors.New("fixture: invalid identity intent")
	}
	before := len(n.records)
	n.records = slices.DeleteFunc(n.records, func(record fixtureRecord) bool {
		return slices.Contains(ids, record.id) && (!guarded || record.metadata == guard.GetStringVal())
	})
	return &milvuspb.MutationResult{Status: &commonpb.Status{}, DeleteCnt: int64(before - len(n.records))}, nil
}

func nativeClient(t *testing.T, service *nativeService) *milvusclient.Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	milvuspb.RegisterMilvusServiceServer(server, service)
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
		if err := <-finished; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Error(err)
		}
	})
	client, err := milvusclient.New(t.Context(), &milvusclient.ClientConfig{Address: "bufnet", DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) })}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return client
}

type testBatcher struct {
	singleton bool
	err       error
}

func (t testBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	if t.err != nil {
		return nil, t.err
	}
	if !t.singleton {
		return [][]*document.Document{docs}, nil
	}
	batches := make([][]*document.Document, len(docs))
	for position, doc := range docs {
		batches[position] = []*document.Document{doc}
	}
	return batches, nil
}

func fixtureModel(vectorFor func(string) []float64) embedding.Model {
	return embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for position, text := range request.Texts {
			vector := []float64{1, 0}
			if vectorFor != nil {
				vector = vectorFor(text)
			}
			outputs[position] = &embedding.Output{Embedding: vector}
		}
		return embedding.NewResponse(outputs, nil)
	})
}

func nativeStore(t *testing.T, metric entity.MetricType, vectorFor func(string) []float64) (*Store, *nativeService) {
	t.Helper()
	service := newNativeService(metric)
	store, err := NewStore(t.Context(), StoreConfig{Client: nativeClient(t, service), CollectionName: "documents", EmbeddingModel: fixtureModel(vectorFor), DocumentBatcher: testBatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	return store, service
}

func indexedStore(t *testing.T, metric entity.MetricType, docs []*document.Document, vectorFor func(string) []float64) (*Store, *nativeService) {
	t.Helper()
	store, service := nativeStore(t, metric, vectorFor)
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(fmt.Errorf("index fixture: %w", err))
	}
	return store, service
}
