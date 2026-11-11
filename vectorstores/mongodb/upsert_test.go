package mongodb

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// countingCollection answers a bulk write with a scripted result. The embedded
// interface stays nil so a call to any other operation fails the test.
type countingCollection struct {
	DocumentCollection
	result *mongo.BulkWriteResult
	sent   int
}

func (c *countingCollection) BulkWrite(
	_ context.Context,
	models []mongo.WriteModel,
	_ ...options.Lister[options.BulkWriteOptions],
) (*mongo.BulkWriteResult, error) {
	c.sent = len(models)
	return c.result, nil
}

func (*countingCollection) Aggregate(context.Context, any, ...options.Lister[options.AggregateOptions]) (*mongo.Cursor, error) {
	return mongo.NewCursorFromDocuments([]any{nativeIndexPolicy(DefaultVectorIndexName, "cosine", 2)}, nil, nil)
}

func upsertStore(t *testing.T, collection DocumentCollection) *Store {
	t.Helper()
	s, err := NewStore(t.Context(), StoreConfig{Collection: collection, Space: fixtureSpace})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// MatchedCount covers the replacements that found an existing document and
// UpsertedCount those that inserted one, so their sum is MongoDB's own count of
// applied writes and anything short of the batch is a partial write.
func TestIndexRequiresEveryReplacementToApply(t *testing.T) {
	t.Parallel()

	request := fixtureBatch([]*document.Document{
		{ID: "one", Text: "first"},
		{ID: "two", Text: "second"},
	})
	for _, sample := range []struct {
		name   string
		result *mongo.BulkWriteResult
		want   string
	}{
		{
			name:   "every document replaced",
			result: &mongo.BulkWriteResult{Acknowledged: true, MatchedCount: 2},
		},
		{
			name:   "one replaced and one inserted",
			result: &mongo.BulkWriteResult{Acknowledged: true, MatchedCount: 1, UpsertedCount: 1},
		},
		{
			name:   "one document unaccounted for",
			result: &mongo.BulkWriteResult{Acknowledged: true, MatchedCount: 1},
			want:   "applied 1 of 2 documents",
		},
		{
			name:   "nothing applied",
			result: &mongo.BulkWriteResult{Acknowledged: true},
			want:   "applied 0 of 2 documents",
		},
		{
			name:   "unacknowledged write concern",
			result: &mongo.BulkWriteResult{},
			want:   "unacknowledged (w: 0)",
		},
		{
			name:   "no result at all",
			result: nil,
			want:   "returned no result",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			collection := &countingCollection{result: sample.result}
			err := upsertStore(t, collection).Upsert(t.Context(), request)
			if collection.sent != 2 {
				t.Fatalf("write models sent = %d, want 2", collection.sent)
			}
			if sample.want == "" {
				if err != nil {
					t.Fatalf("Upsert() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("Upsert() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}

var _ DocumentCollection = (*mongo.Collection)(nil)

type deletionCollection struct {
	DocumentCollection
	result *mongo.DeleteResult
}

func (d *deletionCollection) Aggregate(context.Context, any, ...options.Lister[options.AggregateOptions]) (*mongo.Cursor, error) {
	return mongo.NewCursorFromDocuments([]any{nativeIndexPolicy(DefaultVectorIndexName, "cosine", 2)}, nil, nil)
}

func (d *deletionCollection) DeleteMany(context.Context, any, ...options.Lister[options.DeleteManyOptions]) (*mongo.DeleteResult, error) {
	return d.result, nil
}

func TestDeleteRequiresAcknowledgment(t *testing.T) {
	for _, result := range []*mongo.DeleteResult{nil, {}, {Acknowledged: true}, {Acknowledged: true, DeletedCount: 1}} {
		store := upsertStore(t, &deletionCollection{result: result})
		wantError := result == nil || !result.Acknowledged
		for _, err := range []error{store.DeleteIDs(t.Context(), []string{"one"}), store.DeleteWhere(t.Context(), filter.EQ("tag", "one"))} {
			if (err != nil) != wantError {
				t.Fatalf("result=%v error=%v", result, err)
			}
		}
	}
}

func TestOrderedBulkFailurePreservesOnlyProvedIDs(t *testing.T) {
	ids := []string{"inserted", "uncertain", "rejected", "unsent"}
	failure := mongo.BulkWriteException{WriteErrors: []mongo.BulkWriteError{{WriteError: mongo.WriteError{Index: 2, Code: 11000}}}}
	r := &mongo.BulkWriteResult{UpsertedIDs: map[int64]any{0: "inserted"}, MatchedCount: 2, UpsertedCount: 1}
	w, ok := errors.AsType[*vectorstore.WriteError](writeEvidence(ids, r, failure))
	if !ok {
		t.Fatal("valid bulk evidence did not produce a WriteError")
	}
	if !slices.Equal(w.Outcome().Acknowledged, ids[:1]) || !slices.Equal(w.Outcome().Unconfirmed, ids[1:2]) || !slices.Equal(w.Outcome().Rejected, ids[2:3]) || !slices.Equal(w.Outcome().Unattempted, ids[3:]) || w.Outcome().AcknowledgedCount != nil {
		t.Fatalf("evidence=%+v", w)
	}
	r = &mongo.BulkWriteResult{Acknowledged: true, MatchedCount: 1}
	w, ok = errors.AsType[*vectorstore.WriteError](writeEvidence(ids, r, errors.New("short receipt")))
	if !ok {
		t.Fatal("valid bulk evidence did not produce a WriteError")
	}
	if len(w.Outcome().Acknowledged) != 0 || !slices.Equal(w.Outcome().Unconfirmed, ids) || w.Outcome().AcknowledgedCount == nil || *w.Outcome().AcknowledgedCount != 1 {
		t.Fatalf("evidence=%+v", w)
	}
}
