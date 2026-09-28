package qdrant

import (
	"context"
	"fmt"
	"net"
	"slices"
	"testing"

	qdrantclient "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

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
	points         []*qdrantclient.PointStruct
	deleted        []string
	scrolls        int
	beforeQuery    func()
	ignoreSelected bool
}

func (f *filterPoints) Scroll(_ context.Context, request *qdrantclient.ScrollPoints) (*qdrantclient.ScrollResponse, error) {
	f.scrolls++
	start := 0
	if request.Offset != nil {
		for index, point := range f.points {
			if point.Id.String() == request.Offset.String() {
				start = index
				break
			}
		}
	}
	// One point per page exercises the provider's offset protocol independently
	// of its requested page size.
	end := min(start+1, len(f.points))
	reply := &qdrantclient.ScrollResponse{}
	for _, point := range f.points[start:end] {
		reply.Result = append(reply.Result, &qdrantclient.RetrievedPoint{Id: point.Id, Payload: point.Payload})
	}
	if end < len(f.points) {
		reply.NextPageOffset = f.points[end].Id
	}
	return reply, nil
}

func (f *filterPoints) Query(_ context.Context, request *qdrantclient.QueryPoints) (*qdrantclient.QueryResponse, error) {
	if f.beforeQuery != nil {
		f.beforeQuery()
		f.beforeQuery = nil
	}
	var ids []string
	if request.Filter != nil {
		if len(request.Filter.Must) != 1 || request.Filter.Must[0].GetHasId() == nil {
			return nil, fmt.Errorf("query must restrict selected IDs")
		}
		for _, id := range request.Filter.Must[0].GetHasId().HasId {
			ids = append(ids, id.String())
		}
	}
	reply := &qdrantclient.QueryResponse{}
	for _, point := range f.points {
		if !f.ignoreSelected && ids != nil && !slices.Contains(ids, point.Id.String()) {
			continue
		}
		reply.Result = append(reply.Result, &qdrantclient.ScoredPoint{Id: point.Id, Payload: point.Payload, Score: 1})
		if len(reply.Result) >= int(request.GetLimit()) {
			break
		}
	}
	return reply, nil
}

func (f *filterPoints) Delete(_ context.Context, request *qdrantclient.DeletePoints) (*qdrantclient.PointsOperationResponse, error) {
	for _, id := range request.Points.GetPoints().Ids {
		value, err := formatPointID(id)
		if err != nil {
			return nil, err
		}
		f.deleted = append(f.deleted, value)
	}
	return &qdrantclient.PointsOperationResponse{Result: &qdrantclient.UpdateResult{Status: qdrantclient.UpdateStatus_Completed}}, nil
}

func qdrantFilterStore(t *testing.T, docs []*document.Document) (*Store, *filterPoints) {
	t.Helper()
	fixture := &filterPoints{}
	store := &Store{collectionName: "fixture", distanceMetric: DistanceCosine}
	for _, doc := range docs {
		point, err := store.buildPointStruct(doc, []float64{1, 0})
		if err != nil {
			t.Fatal(err)
		}
		fixture.points = append(fixture.points, point)
	}
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
	t.Cleanup(func() { _ = client.Close(); server.Stop(); <-done })
	store.client = client
	store.embeddingClient, err = embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
	}))
	if err != nil {
		t.Fatal(err)
	}
	return store, fixture
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
	for index, values := range []map[string]any{{}, {"value": nil}, {"value": []any{}}, {"value": []any{"x"}}, {"value": "x"}} {
		encoded, err := metadata.FromValues(values)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, &document.Document{ID: fmt.Sprint(index + 1), Text: "text", Metadata: encoded})
	}
	store, fixture := qdrantFilterStore(t, docs)
	predicate, err := filter.Parse("value is null")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWhere(t.Context(), predicate); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fixture.deleted, []string{"1", "2"}) {
		t.Fatalf("deleted %v", fixture.deleted)
	}
	if fixture.scrolls != len(docs) {
		t.Fatalf("scrolls %d", fixture.scrolls)
	}
}

func TestFilteredSearchRejectsChangedSelection(t *testing.T) {
	for _, changeID := range []bool{false, true} {
		t.Run(fmt.Sprint("change_id_", changeID), func(t *testing.T) {
			values, err := metadata.FromValues(map[string]any{"value": "x"})
			if err != nil {
				t.Fatal(err)
			}
			store, fixture := qdrantFilterStore(t, []*document.Document{{ID: "1", Text: "text", Metadata: values}})
			fixture.beforeQuery = func() {
				if changeID {
					fixture.points[0].Id = &qdrantclient.PointId{PointIdOptions: &qdrantclient.PointId_Num{Num: 2}}
					fixture.ignoreSelected = true
				} else {
					fixture.points[0].Payload["value"] = &qdrantclient.Value{Kind: &qdrantclient.Value_StringValue{StringValue: "y"}}
				}
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
			if err == nil || response != nil {
				t.Fatalf("response %+v, error %v", response, err)
			}
		})
	}
}
