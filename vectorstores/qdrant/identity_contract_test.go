package qdrant

import (
	"context"
	"errors"
	"maps"
	"net"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	qdrantclient "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
)

type identityCollections struct {
	qdrantclient.UnimplementedCollectionsServer
	metric *qdrantclient.Distance
}

func (i *identityCollections) Get(context.Context, *qdrantclient.GetCollectionInfoRequest) (*qdrantclient.GetCollectionInfoResponse, error) {
	metric := qdrantclient.Distance_Cosine
	if i.metric != nil {
		metric = *i.metric
	}
	return &qdrantclient.GetCollectionInfoResponse{Result: collectionInfo(metric, 2)}, nil
}

func (i *identityCollections) List(context.Context, *qdrantclient.ListCollectionsRequest) (*qdrantclient.ListCollectionsResponse, error) {
	return &qdrantclient.ListCollectionsResponse{Collections: []*qdrantclient.CollectionDescription{{Name: "documents"}}}, nil
}

type identityPoints struct {
	qdrantclient.UnimplementedPointsServer
	mu      sync.Mutex
	points  map[string]*qdrantclient.PointStruct
	upserts atomic.Int64
	deletes atomic.Int64
}

func (i *identityPoints) Upsert(_ context.Context, request *qdrantclient.UpsertPoints) (*qdrantclient.PointsOperationResponse, error) {
	i.upserts.Add(1)
	if request.Wait == nil || !*request.Wait {
		return nil, errors.New("native write did not wait for application")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, point := range request.Points {
		id, err := nativeIdentityKey(point.Id)
		if err != nil {
			return nil, err
		}
		if _, isUUID := point.Id.GetPointIdOptions().(*qdrantclient.PointId_Uuid); isUUID {
			point.Id = qdrantclient.NewIDUUID(id)
		}
		i.points[id] = point
	}
	return &qdrantclient.PointsOperationResponse{Result: &qdrantclient.UpdateResult{Status: qdrantclient.UpdateStatus_Completed}}, nil
}

func (i *identityPoints) Query(_ context.Context, request *qdrantclient.QueryPoints) (*qdrantclient.QueryResponse, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	response := &qdrantclient.QueryResponse{}
	for _, id := range slices.Sorted(maps.Keys(i.points)) {
		point := i.points[id]
		response.Result = append(response.Result, &qdrantclient.ScoredPoint{Id: point.Id, Payload: point.Payload, Vectors: nativeOutput(point.Vectors), Score: 1})
		if len(response.Result) >= int(request.GetLimit()) {
			break
		}
	}
	return response, nil
}

func (i *identityPoints) Scroll(_ context.Context, request *qdrantclient.ScrollPoints) (*qdrantclient.ScrollResponse, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	response := &qdrantclient.ScrollResponse{}
	for _, id := range slices.Sorted(maps.Keys(i.points)) {
		point := i.points[id]
		response.Result = append(response.Result, &qdrantclient.RetrievedPoint{Id: point.Id, Payload: point.Payload, Vectors: nativeOutput(point.Vectors)})
	}
	return response, nil
}

func (i *identityPoints) Delete(_ context.Context, request *qdrantclient.DeletePoints) (*qdrantclient.PointsOperationResponse, error) {
	i.deletes.Add(1)
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, pointID := range request.Points.GetPoints().Ids {
		id, err := nativeIdentityKey(pointID)
		if err != nil {
			return nil, err
		}
		delete(i.points, id)
	}
	return &qdrantclient.PointsOperationResponse{Result: &qdrantclient.UpdateResult{Status: qdrantclient.UpdateStatus_Completed}}, nil
}

// Qdrant's gRPC conversions parse UUID input and stringify the native UUID on
// output, so different spellings address one point rather than distinct IDs.
func nativeIdentityKey(id *qdrantclient.PointId) (string, error) {
	switch value := id.GetPointIdOptions().(type) {
	case *qdrantclient.PointId_Num:
		return strconv.FormatUint(value.Num, 10), nil
	case *qdrantclient.PointId_Uuid:
		parsed, err := uuid.Parse(value.Uuid)
		if err != nil {
			return "", err
		}
		return parsed.String(), nil
	default:
		return "", errors.New("native point has no ID")
	}
}

func qdrantIdentityStore(t *testing.T) (*Store, *identityPoints) {
	t.Helper()
	points := &identityPoints{points: make(map[string]*qdrantclient.PointStruct)}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	qdrantclient.RegisterCollectionsServer(server, &identityCollections{})
	qdrantclient.RegisterPointsServer(server, points)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if serveErr := <-done; serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			t.Error(serveErr)
		}
	})
	client, err := qdrantclient.NewClient(&qdrantclient.Config{
		Host: "127.0.0.1", PoolSize: 1, SkipCompatibilityCheck: true,
		GrpcOptions: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})

	store, err := NewStore(t.Context(), StoreConfig{
		Client: client, CollectionName: "documents",
		Space: fixtureSpace,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, points
}

func TestIndexRejectsUUIDAliasesBeforeIO(t *testing.T) {
	for _, alias := range []string{
		"F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		t.Run(alias, func(t *testing.T) {
			store, points := qdrantIdentityStore(t)
			err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{
				{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "canonical"},
				{ID: alias, Text: "alias"},
			}))
			if !errors.Is(err, ErrInvalidPointID) {
				t.Fatalf("Index error = %v, want ErrInvalidPointID", err)
			}
			if points.upserts.Load() != 0 {
				t.Fatalf("rejected batch performed I/O: upserts=%d", points.upserts.Load())
			}
		})
	}
}

func TestDeleteIDsRejectsUUIDAliasesBeforeIO(t *testing.T) {
	for _, alias := range []string{
		"F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		t.Run(alias, func(t *testing.T) {
			store, points := qdrantIdentityStore(t)
			if err := store.DeleteIDs(t.Context(), []string{"42", alias}); !errors.Is(err, ErrInvalidPointID) {
				t.Errorf("DeleteIDs error = %v, want ErrInvalidPointID", err)
			}
			if points.deletes.Load() != 0 {
				t.Fatalf("rejected IDs performed %d deletes", points.deletes.Load())
			}
		})
	}
}

func TestIndexAndSearchPreserveCanonicalPointIDs(t *testing.T) {
	store, _ := qdrantIdentityStore(t)
	documents := []*document.Document{
		{ID: "0", Text: "zero"},
		{ID: "18446744073709551615", Text: "maximum uint64"},
		{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "uuid"},
	}
	if err := store.Upsert(t.Context(), fixtureBatch(documents)); err != nil {
		t.Fatal(err)
	}
	request := &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: 3}}
	response, err := store.Query(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, hit := range response.Results {
		got[hit.Document.ID] = hit.Document.Text
	}
	want := map[string]string{
		"0":                                    "zero",
		"18446744073709551615":                 "maximum uint64",
		"f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4": "uuid",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Search documents = %v, want %v", got, want)
	}
	if deleteErr := store.DeleteIDs(t.Context(), []string{"0", "18446744073709551615", "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"}); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	response, err = store.Query(t.Context(), request)
	if err != nil || len(response.Results) != 0 {
		t.Fatalf("Search after DeleteIDs = %v, error %v", response, err)
	}
}
