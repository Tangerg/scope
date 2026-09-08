package mongodb

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

// scriptedCollection answers writes with a scripted result. The embedded
// interface stays nil so a call to any other operation fails the test.
type scriptedCollection struct {
	MessageCollection
	inserted *mongo.InsertManyResult
	deleted  *mongo.DeleteResult
	calls    int
}

func (s *scriptedCollection) InsertMany(
	_ context.Context,
	_ any,
	_ ...options.Lister[options.InsertManyOptions],
) (*mongo.InsertManyResult, error) {
	s.calls++
	return s.inserted, nil
}

func (s *scriptedCollection) DeleteMany(
	_ context.Context,
	_ any,
	_ ...options.Lister[options.DeleteManyOptions],
) (*mongo.DeleteResult, error) {
	s.calls++
	return s.deleted, nil
}

// MongoDB sends no reply for a w: 0 write, so the driver returns a nil error
// for messages it never learned the fate of. Reporting that as a successful
// append would lose history silently.
func TestWriteRejectsUnacknowledgedResult(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name     string
		inserted *mongo.InsertManyResult
		want     string
	}{
		{
			name:     "acknowledged",
			inserted: &mongo.InsertManyResult{Acknowledged: true, InsertedIDs: []any{1}},
		},
		{
			name:     "unacknowledged",
			inserted: &mongo.InsertManyResult{InsertedIDs: []any{1}},
			want:     "write: collection writes are unacknowledged (w: 0)",
		},
		{
			name:     "no result at all",
			inserted: nil,
			want:     "write: collection writes are unacknowledged (w: 0)",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			collection := &scriptedCollection{inserted: sample.inserted}
			store := &Store{collection: collection}
			err := store.Write(t.Context(), history.ConversationID("conversation"),
				chat.NewUserMessage(chat.NewTextPart("hello")))
			if collection.calls != 1 {
				t.Fatalf("InsertMany calls = %d, want 1", collection.calls)
			}
			if sample.want == "" {
				if err != nil {
					t.Fatalf("Write() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("Write() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}

// Clear promises the conversation is gone, which an unacknowledged delete
// cannot establish either.
func TestClearRejectsUnacknowledgedResult(t *testing.T) {
	t.Parallel()

	collection := &scriptedCollection{deleted: &mongo.DeleteResult{}}
	store := &Store{collection: collection}
	err := store.Clear(t.Context(), history.ConversationID("conversation"))
	if err == nil || !strings.Contains(err.Error(), "clear: collection writes are unacknowledged (w: 0)") {
		t.Fatalf("Clear() = %v, want an unacknowledged-write error", err)
	}

	collection = &scriptedCollection{deleted: &mongo.DeleteResult{Acknowledged: true}}
	store = &Store{collection: collection}
	if err := store.Clear(t.Context(), history.ConversationID("conversation")); err != nil {
		t.Fatalf("Clear() = %v, want nil", err)
	}
}

var _ MessageCollection = (*mongo.Collection)(nil)
