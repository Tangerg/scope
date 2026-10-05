package qdrant

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"slices"
	"testing"

	qdrantclient "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type filterPoints struct {
	qdrantclient.UnimplementedPointsServer
	points                    []*qdrantclient.PointStruct
	deleted                   []string
	scrolls                   int
	upserts                   int
	scores                    map[string]float32
	metric                    qdrantclient.Distance
	beforeQuery, beforeDelete func()
	ignoreSelected            bool
	queryHook                 func(*qdrantclient.QueryPoints) (*qdrantclient.QueryResponse, error)
	scrollHook                func(*qdrantclient.ScrollPoints) (*qdrantclient.ScrollResponse, error)
}

func (f *filterPoints) Upsert(_ context.Context, request *qdrantclient.UpsertPoints) (*qdrantclient.PointsOperationResponse, error) {
	f.upserts++
	if request.Wait == nil || !*request.Wait {
		return nil, fmt.Errorf("native upsert did not wait for application")
	}
	for _, point := range request.Points {
		f.points = append(f.points, proto.Clone(point).(*qdrantclient.PointStruct))
	}
	return &qdrantclient.PointsOperationResponse{Result: &qdrantclient.UpdateResult{Status: qdrantclient.UpdateStatus_Completed}}, nil
}

func (f *filterPoints) Scroll(_ context.Context, request *qdrantclient.ScrollPoints) (*qdrantclient.ScrollResponse, error) {
	f.scrolls++
	if f.scrollHook != nil {
		return f.scrollHook(request)
	}
	start := 0
	if request.Offset != nil {
		for i, point := range f.points {
			if point.Id.String() == request.Offset.String() {
				start = i
				break
			}
		}
	}
	end := min(start+1, len(f.points))
	response := &qdrantclient.ScrollResponse{}
	for _, point := range f.points[start:end] {
		response.Result = append(response.Result, &qdrantclient.RetrievedPoint{Id: point.Id, Payload: point.Payload, Vectors: nativeOutput(point.Vectors)})
	}
	if end < len(f.points) {
		response.NextPageOffset = f.points[end].Id
	}
	return response, nil
}

func (f *filterPoints) Query(_ context.Context, request *qdrantclient.QueryPoints) (*qdrantclient.QueryResponse, error) {
	if f.beforeQuery != nil {
		f.beforeQuery()
		f.beforeQuery = nil
	}
	if f.queryHook != nil {
		return f.queryHook(request)
	}
	if request.ScoreThreshold != nil || !request.WithPayload.GetEnable() || !request.WithVectors.GetEnable() {
		return nil, fmt.Errorf("native query omitted complete records or duplicated score normalization")
	}
	response := &qdrantclient.QueryResponse{}
	for _, point := range f.points {
		if !f.ignoreSelected && !nativeMetadataSelected(point, request.Filter) {
			continue
		}
		id, err := formatPointID(point.Id)
		if err != nil {
			return nil, err
		}
		score, ok := f.scores[id]
		if !ok {
			score = 1
		}
		response.Result = append(response.Result, &qdrantclient.ScoredPoint{Id: point.Id, Payload: point.Payload, Vectors: nativeOutput(point.Vectors), Score: score})
	}
	slices.SortFunc(response.Result, func(left, right *qdrantclient.ScoredPoint) int {
		order := cmp.Compare(right.Score, left.Score)
		if f.metric == qdrantclient.Distance_Euclid || f.metric == qdrantclient.Distance_Manhattan {
			order = -order
		}
		return order
	})
	response.Result = response.Result[:min(len(response.Result), int(request.GetLimit()))]
	return response, nil
}

func (f *filterPoints) Delete(_ context.Context, request *qdrantclient.DeletePoints) (*qdrantclient.PointsOperationResponse, error) {
	if f.beforeDelete != nil {
		f.beforeDelete()
		f.beforeDelete = nil
	}
	if request.Wait == nil || !*request.Wait {
		return nil, fmt.Errorf("native delete did not wait for application")
	}
	var retained []*qdrantclient.PointStruct
	for _, point := range f.points {
		selected := false
		if request.Points.GetFilter() != nil {
			selected = nativeMetadataSelected(point, request.Points.GetFilter())
		} else {
			for _, id := range request.Points.GetPoints().Ids {
				if proto.Equal(id, point.Id) {
					selected = true
				}
			}
		}
		if selected {
			id, err := formatPointID(point.Id)
			if err != nil {
				return nil, err
			}
			f.deleted = append(f.deleted, id)
		} else {
			retained = append(retained, point)
		}
	}
	f.points = retained
	return &qdrantclient.PointsOperationResponse{Result: &qdrantclient.UpdateResult{Status: qdrantclient.UpdateStatus_Completed}}, nil
}

// The native fixture knows only exact string membership and scripted scores.
// Core predicates and vector similarity are never evaluated here.
func nativeMetadataSelected(point *qdrantclient.PointStruct, selection *qdrantclient.Filter) bool {
	if selection == nil {
		return true
	}
	if len(selection.Must) != 1 {
		return false
	}
	field := selection.Must[0].GetField()
	return field.Key == metadataField && slices.Contains(field.Match.GetKeywords().Strings, point.Payload[metadataField].GetStringValue())
}

func nativeOutput(vectors *qdrantclient.Vectors) *qdrantclient.VectorsOutput {
	return &qdrantclient.VectorsOutput{VectorsOptions: &qdrantclient.VectorsOutput_Vector{Vector: &qdrantclient.VectorOutput{Vector: &qdrantclient.VectorOutput_Dense{Dense: vectors.GetVector().GetDense()}}}}
}

func qdrantFilterStore(t *testing.T, docs []*document.Document) (*Store, *filterPoints) {
	t.Helper()
	fixture := &filterPoints{scores: make(map[string]float32), metric: qdrantclient.Distance_Cosine}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	qdrantclient.RegisterPointsServer(server, fixture)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	client, err := qdrantclient.NewClient(&qdrantclient.Config{Host: "127.0.0.1", PoolSize: 1, SkipCompatibilityCheck: true, GrpcOptions: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) })}})
	if err != nil {
		server.Stop()
		<-done
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		server.Stop()
		if serveErr := <-done; serveErr != nil {
			t.Error(serveErr)
		}
	})
	model, err := embeddingclient.New(embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	}))
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{client: client, collectionName: "fixture", schema: nativeSchema{dimensions: 2, metric: fixture.metric}, embeddingClient: model, documentBatcher: visibilityBatcher{}}
	if len(docs) > 0 {
		if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
			t.Fatal(err)
		}
	}
	return store, fixture
}

func collectionInfo(distance qdrantclient.Distance, dimensions uint64) *qdrantclient.CollectionInfo {
	return &qdrantclient.CollectionInfo{Config: &qdrantclient.CollectionConfig{Params: &qdrantclient.CollectionParams{VectorsConfig: qdrantclient.NewVectorsConfig(&qdrantclient.VectorParams{Size: dimensions, Distance: distance})}}}
}

func TestFilterConformance(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		store, _ := qdrantFilterStore(t, docs)
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, hit := range response.Results {
			ids = append(ids, hit.Document.ID)
		}
		return ids, nil
	}})
}

func TestDeleteWherePreservesEmptyArraysForNull(t *testing.T) {
	var docs []*document.Document
	for i, values := range []map[string]any{{}, {"value": nil}, {"value": []any{}}, {"value": []any{"x"}}, {"value": "x"}} {
		facts, err := metadata.FromValues(values)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, &document.Document{ID: fmt.Sprint(i + 1), Text: "text", Metadata: facts})
	}
	store, fixture := qdrantFilterStore(t, docs)
	if err := store.DeleteWhere(t.Context(), filter.IsNull("value")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fixture.deleted, []string{"1", "2"}) || fixture.scrolls != len(docs) {
		t.Fatalf("deleted=%v scrolls=%d", fixture.deleted, fixture.scrolls)
	}
}

func TestNativeQueryRejectsMetadataOutsideSelection(t *testing.T) {
	facts, err := metadata.FromValues(map[string]any{"value": "x"})
	if err != nil {
		t.Fatal(err)
	}
	store, fixture := qdrantFilterStore(t, []*document.Document{{ID: "1", Text: "text", Metadata: facts}})
	fixture.beforeQuery = func() {
		fixture.points[0].Payload[metadataField] = &qdrantclient.Value{Kind: &qdrantclient.Value_StringValue{StringValue: `{"value":"y"}`}}
		fixture.ignoreSelected = true
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
	if err == nil || response != nil {
		t.Fatalf("response=%v error=%v", response, err)
	}
}
