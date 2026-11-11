package elasticsearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/vectorstores/protocol/elastic"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestEveryMutationHonorsBulkAdmission(t *testing.T) {
	for _, operation := range []struct {
		name   string
		mutate func(context.Context, *elastic.Store, []*document.Document) error
		wire   func(string) string
	}{
		{
			name: "upsert",
			mutate: func(ctx context.Context, store *elastic.Store, docs []*document.Document) error {
				return store.Upsert(ctx, fixtureBatch(docs))
			},
			wire: func(id string) string {
				return fmt.Sprintf("{\"index\":{\"_index\":\"documents\",\"_id\":%q}}\n{\"content\":\"text\",\"embedding\":[1,0],\"metadata_json\":\"null\"}\n", id)
			},
		},
		{
			name: "delete IDs",
			mutate: func(ctx context.Context, store *elastic.Store, docs []*document.Document) error {
				var ids []string
				for _, doc := range docs {
					ids = append(ids, doc.ID)
				}
				return store.DeleteIDs(ctx, ids)
			},
			wire: func(id string) string {
				return fmt.Sprintf("{\"delete\":{\"_index\":\"documents\",\"_id\":%q}}\n", id)
			},
		},
		{
			name: "conditional delete",
			mutate: func(ctx context.Context, store *elastic.Store, _ []*document.Document) error {
				return store.DeleteMatching(ctx, filter.IsNull("unused"), fixtureBudget())
			},
			wire: func(id string) string {
				return fmt.Sprintf("{\"delete\":{\"_index\":\"documents\",\"_id\":%q,\"if_seq_no\":3,\"if_primary_term\":1}}\n", id)
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
			t.Run("exact bytes and framing", func(t *testing.T) {
				docs := []*document.Document{{ID: "a", Text: "text"}, {ID: "b", Text: "text"}}
				limit := len(operation.wire("a"))
				fixture, store := newBulkBudgetFixture(t, limit, docs)
				if err := operation.mutate(t.Context(), store, docs); err != nil {
					t.Fatal(err)
				}
				if len(fixture.bulkBodies) != len(docs) {
					t.Fatalf("expected one complete action per request, got %d requests", len(fixture.bulkBodies))
				}
				for i, body := range fixture.bulkBodies {
					if string(body) != operation.wire(docs[i].ID) || len(body) > limit {
						t.Fatalf("request lost framing, order, budget or condition: %s", body)
					}
				}
			})
			t.Run("one byte below action", func(t *testing.T) {
				docs := []*document.Document{{ID: "a", Text: "text"}}
				fixture, store := newBulkBudgetFixture(t, len(operation.wire("a"))-1, docs)
				err := operation.mutate(t.Context(), store, docs)
				if err == nil || len(fixture.bulkBodies) != 0 || len(fixture.rows) != len(docs) || fixture.writes != len(docs) {
					t.Fatalf("oversized action reached publication: requests %d, writes %d, error %v", len(fixture.bulkBodies), fixture.writes, err)
				}
				if _, ok := errors.AsType[*vectorstore.WriteError](err); ok {
					t.Fatal("preflight failure claimed an attempted write")
				}
			})
			t.Run("late oversized action", func(t *testing.T) {
				docs := []*document.Document{
					{ID: "a", Text: "text"},
					{ID: "b", Text: "text"},
					{ID: strings.Repeat("z", 100), Text: "text"},
				}
				fixture, store := newBulkBudgetFixture(t, len(operation.wire("a")), docs)
				err := operation.mutate(t.Context(), store, docs)
				if err == nil || len(fixture.bulkBodies) != 0 || len(fixture.rows) != len(docs) || fixture.writes != len(docs) {
					t.Fatalf("late oversized action published a prefix: requests %d, writes %d, error %v", len(fixture.bulkBodies), fixture.writes, err)
				}
			})
			t.Run("action count bounds acknowledgment growth", func(t *testing.T) {
				var docs []*document.Document
				for i := range 513 {
					docs = append(docs, &document.Document{ID: fmt.Sprintf("%03d", i), Text: "text"})
				}
				fixture, store := newBulkBudgetFixture(t, 1<<20, docs)
				if err := operation.mutate(t.Context(), store, docs); err != nil {
					t.Fatal(err)
				}
				var counts []int
				linesPerAction := strings.Count(operation.wire("a"), "\n")
				for _, body := range fixture.bulkBodies {
					counts = append(counts, bytes.Count(body, []byte{'\n'})/linesPerAction)
				}
				if !slices.Equal(counts, []int{512, 1}) {
					t.Fatalf("unbounded native acknowledgment batch: %v", counts)
				}
			})
		})
	}
}

func TestBulkBudgetRetainsWriteEvidenceAcrossRequests(t *testing.T) {
	fixture := newProtocolFixture(t)
	const oneAction = "{\"index\":{\"_index\":\"documents\",\"_id\":\"a\"}}\n{\"content\":\"text\",\"embedding\":[1,0],\"metadata_json\":\"null\"}\n"
	store, err := NewStore(t.Context(), StoreConfig{Client: fixture.client, IndexName: "documents", Space: fixtureSpace, MaxBulkBytes: len(oneAction)})
	if err != nil {
		t.Fatal(err)
	}
	fixture.bulkFailureAt = 2
	err = store.Upsert(t.Context(), fixtureBatch([]*document.Document{
		{ID: "a", Text: "text"},
		{ID: "b", Text: "text"},
		{ID: "c", Text: "text"},
	}))
	failure, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok {
		t.Fatalf("missing per-ID evidence: %v", err)
	}
	outcome := failure.Outcome()
	if !slices.Equal(outcome.Acknowledged, []string{"a"}) || !slices.Equal(outcome.Unconfirmed, []string{"b"}) || !slices.Equal(outcome.Unattempted, []string{"c"}) || len(outcome.Rejected) != 0 {
		t.Fatalf("batch boundaries changed write evidence: %+v", outcome)
	}
	if len(fixture.bulkBodies) != 2 || len(fixture.rows) != 1 {
		t.Fatalf("unexpected publication after failure: requests %d, rows %d", len(fixture.bulkBodies), len(fixture.rows))
	}
}

func newBulkBudgetFixture(t *testing.T, limit int, docs []*document.Document) (*protocolFixture, *elastic.Store) {
	t.Helper()
	fixture := newProtocolFixture(t)
	fixture.pageSize = 512
	if err := fixture.store().Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.Context(), StoreConfig{Client: fixture.client, IndexName: "documents", Space: fixtureSpace, MaxBulkBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	fixture.bulkBodies = nil
	return fixture, store
}
