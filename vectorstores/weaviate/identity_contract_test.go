package weaviate

import (
	"errors"
	"reflect"
	"testing"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestIndexRejectsUUIDAliasesBeforeIO(t *testing.T) {
	for _, alias := range []string{
		"00000000-0000-0000-0000-000000000000",
		"F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		t.Run(alias, func(t *testing.T) {
			fixture := &nativeFixture{}
			store := newNativeStore(t, fixture)
			err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{
				{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "canonical"},
				{ID: alias, Text: "alias"},
			}))
			if !errors.Is(err, ErrInvalidObjectID) {
				t.Fatalf("Index error = %v, want ErrInvalidObjectID", err)
			}
			if fixture.batches.Load() != 0 || fixture.scans.Load() != 0 {
				t.Fatalf("rejected batch performed I/O: scans=%d, batches=%d", fixture.scans.Load(), fixture.batches.Load())
			}
		})
	}
}

func TestDeleteIDsRejectsUUIDAliasesBeforeIO(t *testing.T) {
	for _, alias := range []string{
		"00000000-0000-0000-0000-000000000000",
		"F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4",
		"f9168c5eceb24faab6bf329bf39fa1e4",
		"urn:uuid:f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4",
		"{f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4}",
	} {
		t.Run(alias, func(t *testing.T) {
			fixture := &nativeFixture{}
			store := newNativeStore(t, fixture)
			if err := store.DeleteIDs(t.Context(), []string{"12345678-1234-1234-1234-123456789012", alias}); !errors.Is(err, ErrInvalidObjectID) {
				t.Errorf("DeleteIDs error = %v, want ErrInvalidObjectID", err)
			}
			if fixture.deletes.Load() != 0 {
				t.Fatalf("rejected IDs performed %d deletes", fixture.deletes.Load())
			}
		})
	}
}

// gRPC UUIDs are binary: aliases cannot occur in responses. Missing,
// malformed and zero identifiers must not become a document identity.
func TestQueryRejectsMissingOrMalformedUUID(t *testing.T) {
	for _, raw := range [][]byte{nil, {1, 2}, make([]byte, 16)} {
		s, f := indexedNativeStore(t, []*document.Document{{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "canonical"}})
		f.mu.Lock()
		f.queryChange = func(rows []*pb.SearchResult) []*pb.SearchResult { rows[0].Metadata.IdAsBytes = raw; return rows }
		f.mu.Unlock()
		out, err := s.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q")})
		if err == nil || out != nil {
			t.Fatalf("invalid wire UUID %x accepted: %v", raw, out)
		}
	}
}

func TestIndexAndSearchPreserveCanonicalObjectIDs(t *testing.T) {
	store := newNativeStore(t, &nativeFixture{})
	if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{
		{ID: "12345678-1234-1234-1234-123456789012", Text: "first"},
		{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "second"},
	})); err != nil {
		t.Fatal(err)
	}
	request := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 2}}
	response, err := store.Query(t.Context(), request)
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
	if err = store.DeleteIDs(t.Context(), []string{"12345678-1234-1234-1234-123456789012"}); err != nil {
		t.Fatalf("repeated delete: %v", err)
	}
	response, err = store.Query(t.Context(), request)
	if err != nil || len(response.Results) != 0 {
		t.Fatalf("Search after DeleteIDs = %v, error %v", response, err)
	}
}
