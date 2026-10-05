package weaviate

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestIndexRejectsUUIDAliasesBeforeIO(t *testing.T) {
	for _, alias := range []string{
		"F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		t.Run(alias, func(t *testing.T) {
			fixture := &nativeFixture{}
			store, embeddings := newNativeStore(t, fixture)
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
			fixture := &nativeFixture{}
			store, _ := newNativeStore(t, fixture)
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
			store, _ := newNativeStore(t, &nativeFixture{acknowledge: sample.acknowledge})
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
			store, _ := newNativeStore(t, &nativeFixture{queryID: &id})
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

func TestIndexAndSearchPreserveCanonicalObjectIDs(t *testing.T) {
	store, _ := newNativeStore(t, &nativeFixture{})
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
