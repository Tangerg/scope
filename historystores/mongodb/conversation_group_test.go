package mongodb

import (
	"context"
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/history"
)

// groupingCollection records the pipeline it was handed and answers from
// pre-loaded documents. The embedded interface stays nil so a call to any
// other operation fails the test.
type groupingCollection struct {
	MessageCollection
	documents []any
	pipeline  any
}

func (g *groupingCollection) Aggregate(
	_ context.Context,
	pipeline any,
	_ ...options.Lister[options.AggregateOptions],
) (*mongo.Cursor, error) {
	g.pipeline = pipeline
	return mongo.NewCursorFromDocuments(g.documents, nil, nil)
}

// MongoDB documents three limits on the distinct command that all share one
// remedy: on a sharded cluster it "may return orphaned documents", its single
// result document is bounded by the maximum BSON size, and it is unavailable
// on a sharded collection inside a transaction. For each, MongoDB says to "use
// the aggregation pipeline with the $group stage instead". This asserts the
// store asks the question that way, because the two commands differ in what
// they return, not in what they mean.
func TestConversationsGroupsRatherThanUsingDistinct(t *testing.T) {
	t.Parallel()

	collection := &groupingCollection{documents: []any{
		bson.D{{Key: "_id", Value: "gamma"}},
		bson.D{{Key: "_id", Value: "alpha"}},
		bson.D{{Key: "_id", Value: "beta"}},
	}}

	ids, err := storeFor(t, collection).Conversations(t.Context())
	if err != nil {
		t.Fatalf("Conversations() = %v, want nil", err)
	}
	if want := []history.ConversationID{"alpha", "beta", "gamma"}; !slices.Equal(ids, want) {
		t.Fatalf("Conversations() = %v, want %v", ids, want)
	}

	pipeline, ok := collection.pipeline.(mongo.Pipeline)
	if !ok {
		t.Fatalf("pipeline has type %T, want mongo.Pipeline", collection.pipeline)
	}
	want := mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.D{{Key: fieldID, Value: "$" + fieldConversationID}}}},
	}
	if len(pipeline) != len(want) || pipeline[0][0].Key != want[0][0].Key {
		t.Fatalf("pipeline = %v, want a single $group stage", pipeline)
	}
	group, ok := pipeline[0][0].Value.(bson.D)
	if !ok || len(group) != 1 || group[0].Key != fieldID || group[0].Value != "$"+fieldConversationID {
		t.Fatalf("$group stage = %v, want grouping by $%s", pipeline[0][0].Value, fieldConversationID)
	}
}

// An empty store yields a non-nil empty slice, which Lister requires.
func TestConversationsReturnsNonNilForAnEmptyStore(t *testing.T) {
	t.Parallel()

	ids, err := storeFor(t, &groupingCollection{}).Conversations(t.Context())
	if err != nil {
		t.Fatalf("Conversations() = %v, want nil", err)
	}
	if ids == nil || len(ids) != 0 {
		t.Fatalf("Conversations() = %v, want a non-nil empty slice", ids)
	}
}

// A stored id the store would refuse to write is refused on the way out too,
// rather than handed to a caller that cannot use it.
func TestConversationsRefusesAnInvalidStoredID(t *testing.T) {
	t.Parallel()

	collection := &groupingCollection{documents: []any{bson.D{{Key: "_id", Value: ""}}}}
	if _, err := storeFor(t, collection).Conversations(t.Context()); err == nil {
		t.Fatal("Conversations() = nil error, want the invalid stored ID reported")
	}
}
