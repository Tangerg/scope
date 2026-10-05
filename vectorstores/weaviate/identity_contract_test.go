package weaviate

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-openapi/strfmt"
	"github.com/google/uuid"
	weaviateclient "github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type identityFixture struct {
	t           *testing.T
	mu          sync.Mutex
	objects     map[string]*models.Object
	batches     atomic.Int64
	deletes     atomic.Int64
	acknowledge func([]models.ObjectsGetResponse) []models.ObjectsGetResponse
	queryID     *string
}

func (i *identityFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	var response any
	switch {
	case request.URL.Path == "/v1/meta":
		response = map[string]any{"version": "1.39.3"}
	case request.Method == http.MethodGet && request.URL.Path == "/v1/schema/Documents":
		response = &models.Class{Class: "Documents", VectorIndexConfig: map[string]any{"distance": "cosine"}, Properties: []*models.Property{
			{Name: "content", DataType: []string{"text"}, Tokenization: "word"},
			{Name: "metadata", DataType: []string{"text"}},
		}}
	case request.Method == http.MethodPost && request.URL.Path == "/v1/batch/objects":
		i.batches.Add(1)
		var body struct {
			Objects []*models.Object `json:"objects"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			i.t.Error(err)
			http.Error(writer, "invalid objects", http.StatusBadRequest)
			return
		}
		responses := make([]models.ObjectsGetResponse, 0, len(body.Objects))
		for _, object := range body.Objects {
			parsed, err := uuid.Parse(object.ID.String())
			if err != nil {
				i.t.Error(err)
				http.Error(writer, "invalid UUID", http.StatusBadRequest)
				return
			}
			// Native storage keys contain UUID bytes; readback formats those bytes.
			stored := *object
			stored.ID = strfmt.UUID(parsed.String())
			i.objects[stored.ID.String()] = &stored
			responses = append(responses, models.ObjectsGetResponse{Object: *object, Result: batchResult(models.ObjectsGetResponseAO2ResultStatusSUCCESS, nil)})
		}
		if i.acknowledge != nil {
			responses = i.acknowledge(responses)
		}
		response = responses
	case request.Method == http.MethodPost && request.URL.Path == "/v1/graphql":
		items := make([]map[string]any, 0, len(i.objects))
		for _, id := range slices.Sorted(maps.Keys(i.objects)) {
			object := i.objects[id]
			properties := object.Properties.(map[string]any)
			if i.queryID != nil {
				id = *i.queryID
			}
			items = append(items, map[string]any{
				"content": properties["content"], "metadata": properties["metadata"],
				"_additional": map[string]any{"id": id, "distance": 0},
			})
		}
		response = map[string]any{"data": map[string]any{"Get": map[string]any{"Documents": items}}}
	case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/objects/Documents/"):
		i.deletes.Add(1)
		id := strings.TrimPrefix(request.URL.Path, "/v1/objects/Documents/")
		parsed, err := uuid.Parse(id)
		if err != nil {
			i.t.Error(err)
			http.Error(writer, "invalid UUID", http.StatusBadRequest)
			return
		}
		delete(i.objects, parsed.String())
		writer.WriteHeader(http.StatusNoContent)
		return
	default:
		i.t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
		http.NotFound(writer, request)
		return
	}
	if err := jsonv2.MarshalWrite(writer, response); err != nil {
		i.t.Error(err)
	}
}

func weaviateIdentityStore(t *testing.T, fixture *identityFixture) (*Store, *atomic.Int64) {
	t.Helper()
	fixture.t = t
	fixture.objects = make(map[string]*models.Object)
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := weaviateclient.NewClient(weaviateclient.Config{Host: strings.TrimPrefix(server.URL, "http://"), Scheme: "http"})
	if err != nil {
		t.Fatal(err)
	}
	var embeddings atomic.Int64
	store, err := NewStore(t.Context(), StoreConfig{
		Client: client, ClassName: "Documents", DocumentBatcher: testBatcher{},
		EmbeddingModel: embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
			embeddings.Add(1)
			outputs := make([]*embedding.Output, len(request.Texts))
			for index := range outputs {
				outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
			}
			return embedding.NewResponse(outputs, nil)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, &embeddings
}

func TestIndexRejectsUUIDAliasesBeforeIO(t *testing.T) {
	for _, alias := range []string{
		"F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		t.Run(alias, func(t *testing.T) {
			fixture := &identityFixture{}
			store, embeddings := weaviateIdentityStore(t, fixture)
			err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{
				{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "canonical"},
				{ID: alias, Text: "alias"},
			}})
			if !errors.Is(err, ErrInvalidObjectID) {
				t.Fatalf("Index error = %v, want ErrInvalidObjectID", err)
			}
			if embeddings.Load() != 0 || fixture.batches.Load() != 0 {
				t.Fatalf("rejected batch performed I/O: embeddings=%d, batches=%d", embeddings.Load(), fixture.batches.Load())
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
			fixture := &identityFixture{}
			store, _ := weaviateIdentityStore(t, fixture)
			if err := store.DeleteIDs(t.Context(), []string{"12345678-1234-1234-1234-123456789012", alias}); !errors.Is(err, ErrInvalidObjectID) {
				t.Errorf("DeleteIDs error = %v, want ErrInvalidObjectID", err)
			}
			if fixture.deletes.Load() != 0 {
				t.Fatalf("rejected IDs performed %d deletes", fixture.deletes.Load())
			}
		})
	}
}

func TestIndexAcknowledgmentsIdentifyEveryRequestedObject(t *testing.T) {
	for _, sample := range []struct {
		name        string
		acknowledge func([]models.ObjectsGetResponse) []models.ObjectsGetResponse
		wantError   bool
	}{
		{name: "requested IDs"},
		{name: "reordered IDs", acknowledge: func(responses []models.ObjectsGetResponse) []models.ObjectsGetResponse {
			slices.Reverse(responses)
			return responses
		}},
		{name: "duplicate ID", wantError: true, acknowledge: func(responses []models.ObjectsGetResponse) []models.ObjectsGetResponse {
			responses[1].ID = responses[0].ID
			return responses
		}},
		{name: "unrequested ID", wantError: true, acknowledge: func(responses []models.ObjectsGetResponse) []models.ObjectsGetResponse {
			responses[1].ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			return responses
		}},
		{name: "missing ID", wantError: true, acknowledge: func(responses []models.ObjectsGetResponse) []models.ObjectsGetResponse {
			responses[1].ID = ""
			return responses
		}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store, _ := weaviateIdentityStore(t, &identityFixture{acknowledge: sample.acknowledge})
			err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{
				{ID: "12345678-1234-1234-1234-123456789012", Text: "first"},
				{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "second"},
			}})
			if (err != nil) != sample.wantError {
				t.Fatalf("Index error = %v, want error %v", err, sample.wantError)
			}
		})
	}
}

func TestSearchRejectsNonCanonicalObjectIDs(t *testing.T) {
	for _, id := range []string{
		"", "document-one", "42", "F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		t.Run(id, func(t *testing.T) {
			store, _ := weaviateIdentityStore(t, &identityFixture{queryID: &id})
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{
				{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "canonical"},
			}}); err != nil {
				t.Fatal(err)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
			if !errors.Is(err, ErrInvalidObjectID) || response != nil {
				t.Fatalf("Search = %v, error %v, want nil response and ErrInvalidObjectID", response, err)
			}
		})
	}
}

func TestSelectionRejectsNonCanonicalObjectIDsBeforeMutation(t *testing.T) {
	for _, id := range []string{
		"F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		t.Run(id, func(t *testing.T) {
			for _, deletion := range []bool{false, true} {
				name := "search"
				if deletion {
					name = "delete"
				}
				t.Run(name, func(t *testing.T) {
					store, fixture := newFilterStore(t, []*document.Document{{ID: id, Text: "stored"}}, "")
					predicate := filter.EQ("value", "x")
					var err error
					if deletion {
						err = store.DeleteWhere(t.Context(), predicate)
					} else {
						_, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: predicate}})
					}
					if !errors.Is(err, ErrInvalidObjectID) {
						t.Fatalf("selection error = %v, want ErrInvalidObjectID", err)
					}
					deleted, queries := fixture.snapshot()
					if len(deleted) != 0 || len(queries) != 1 {
						t.Fatalf("invalid selection continued: deletes=%v, queries=%v", deleted, queries)
					}
				})
			}
		})
	}
}

func TestIndexAndSearchPreserveCanonicalObjectIDs(t *testing.T) {
	store, _ := weaviateIdentityStore(t, &identityFixture{})
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{
		{ID: "12345678-1234-1234-1234-123456789012", Text: "first"},
		{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "second"},
	}}); err != nil {
		t.Fatal(err)
	}
	request := &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 2}}
	response, err := store.Search(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, hit := range response.Results {
		got[hit.Document.ID] = hit.Document.Text
	}
	want := map[string]string{"12345678-1234-1234-1234-123456789012": "first", "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4": "second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Search documents = %v, want %v", got, want)
	}
	if deleteErr := store.DeleteIDs(t.Context(), []string{"12345678-1234-1234-1234-123456789012", "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"}); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	response, err = store.Search(t.Context(), request)
	if err != nil || len(response.Results) != 0 {
		t.Fatalf("Search after DeleteIDs = %v, error %v", response, err)
	}
}
