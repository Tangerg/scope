package mongodb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

// scriptedCollection answers writes with a scripted result. The embedded
// interface stays nil so a call to any other operation fails the test.
type scriptedCollection struct {
	MessageCollection
	inserted    *mongo.InsertManyResult
	insertError error
	deleted     *mongo.DeleteResult
	calls       int
}

func (s *scriptedCollection) InsertMany(
	_ context.Context,
	_ any,
	_ ...options.Lister[options.InsertManyOptions],
) (*mongo.InsertManyResult, error) {
	s.calls++
	return s.inserted, s.insertError
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
// storeFor builds the same Store NewStore would, minus the provider handle.
// The sequence is a dependency rather than a zero value, so a literal cannot
// stand in for it.
func storeFor(t *testing.T, collection MessageCollection) *Store {
	t.Helper()
	sequence, err := history.NewSequence(time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{collection: collection, sequence: sequence}
}

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
			store := storeFor(t, collection)
			outcome, err := store.Write(t.Context(), history.ConversationID("conversation"),
				chat.NewUserMessage(chat.NewTextPart("hello")))
			if collection.calls != 1 {
				t.Fatalf("InsertMany calls = %d, want 1", collection.calls)
			}
			if sample.want == "" {
				if err != nil || outcome != (history.WriteOutcome{Accepted: 1}) {
					t.Fatalf("Write() = %+v, %v", outcome, err)
				}
				return
			}
			if err == nil || outcome != (history.WriteOutcome{Uncertain: true}) || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("Write() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}

// insertMany is ordered and not atomic across documents, so a rejected batch
// leaves the documents before the rejected one stored. Returning the driver's
// outcome must retain that prefix without requiring error-string parsing.
func TestWriteReportsThePrefixARejectedBatchLeftBehind(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name        string
		insertError error
		want        history.WriteOutcome
	}{
		{
			name: "third message rejected",
			insertError: mongo.BulkWriteException{
				WriteErrors: []mongo.BulkWriteError{{WriteError: mongo.WriteError{Index: 2, Code: 11000}}},
			},
			want: history.WriteOutcome{Accepted: 2},
		},
		{
			// A write-concern error establishes nothing about how far the
			// insert got, so the error says that rather than guessing.
			name:        "write concern error",
			insertError: mongo.BulkWriteException{WriteConcernError: &mongo.WriteConcernError{Code: 64}},
			want:        history.WriteOutcome{Uncertain: true},
		},
		{
			name:        "rejection with unconfirmed durability",
			insertError: mongo.BulkWriteException{WriteConcernError: &mongo.WriteConcernError{Code: 64}, WriteErrors: []mongo.BulkWriteError{{WriteError: mongo.WriteError{Index: 2, Code: 11000}}}},
			want:        history.WriteOutcome{Uncertain: true},
		},
		{
			name:        "connection lost",
			insertError: errors.New("connection(localhost:27017) socket was unexpectedly closed"),
			want:        history.WriteOutcome{Uncertain: true},
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			collection := &scriptedCollection{insertError: sample.insertError}
			store := storeFor(t, collection)
			messages := make([]chat.Message, 4)
			for index := range messages {
				messages[index] = chat.NewUserMessage(chat.NewTextPart("hello"))
			}
			outcome, err := store.Write(t.Context(), history.ConversationID("conversation"), messages...)
			if err == nil || outcome != sample.want {
				t.Fatalf("Write() = %+v, %v, want %+v", outcome, err, sample.want)
			}
			// The prefix is added alongside the provider's error, not in
			// place of it, so the chain still reaches what MongoDB said.
			var bulk mongo.BulkWriteException
			if !errors.As(err, &bulk) && !errors.Is(err, sample.insertError) {
				t.Fatalf("Write() = %v, want the provider failure still in the chain", err)
			}
		})
	}
}

// Clear promises the conversation is gone, which an unacknowledged delete
// cannot establish either.
func TestClearRejectsUnacknowledgedResult(t *testing.T) {
	t.Parallel()

	collection := &scriptedCollection{deleted: &mongo.DeleteResult{}}
	store := storeFor(t, collection)
	err := store.Clear(t.Context(), history.ConversationID("conversation"))
	if err == nil || !strings.Contains(err.Error(), "clear: collection writes are unacknowledged (w: 0)") {
		t.Fatalf("Clear() = %v, want an unacknowledged-write error", err)
	}

	collection = &scriptedCollection{deleted: &mongo.DeleteResult{Acknowledged: true}}
	store = storeFor(t, collection)
	if err := store.Clear(t.Context(), history.ConversationID("conversation")); err != nil {
		t.Fatalf("Clear() = %v, want nil", err)
	}
}

var _ MessageCollection = (*mongo.Collection)(nil)
