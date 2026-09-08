package mongodb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

const (
	fieldID             = "_id"
	fieldConversationID = "conversation_id"
	fieldSequence       = "seq"
	fieldMessage        = "message"
	fieldCreatedAt      = "created_at"
)

// MessageCollection is the MongoDB surface the store uses. A
// [mongo.Collection] satisfies it. Naming only the five operations keeps the
// store's write accounting checkable without a running server.
type MessageCollection interface {
	InsertMany(
		ctx context.Context,
		documents any,
		opts ...options.Lister[options.InsertManyOptions],
	) (*mongo.InsertManyResult, error)
	Find(
		ctx context.Context,
		filter any,
		opts ...options.Lister[options.FindOptions],
	) (*mongo.Cursor, error)
	DeleteMany(
		ctx context.Context,
		filter any,
		opts ...options.Lister[options.DeleteManyOptions],
	) (*mongo.DeleteResult, error)
	Distinct(
		ctx context.Context,
		fieldName string,
		filter any,
		opts ...options.Lister[options.DistinctOptions],
	) *mongo.DistinctResult
	Indexes() mongo.IndexView
}

// StoreConfig names every dependency explicitly rather than defaulting a
// client or connection, so a store cannot be built against a service the
// caller did not choose.
type StoreConfig struct {
	// Collection is the live MongoDB collection. Required. The store
	// does not take ownership of the underlying client.
	Collection MessageCollection

	// InitializeSchema, when true, ensures an index on
	// (conversation_id, seq, _id) exists. Idempotent.
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Collection) {
		return errors.New("mongodb: collection is required")
	}
	return nil
}

var (
	_ history.Store  = (*Store)(nil)
	_ history.Lister = (*Store)(nil)
)

// Store persists messages as independently addressable MongoDB documents
// through a caller-owned collection. A local monotonic sequence and ObjectID
// provide deterministic read order; ordering across separate Store values
// remains unspecified.
type Store struct {
	collection MessageCollection
	sequence   sequenceGenerator
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its collection and search index exist
// would fail on the first index rather than at wiring, where the
// misconfiguration actually is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	s := &Store{collection: config.Collection}
	if config.InitializeSchema {
		if err := s.initIndex(ctx); err != nil {
			return nil, fmt.Errorf("mongodb: initialize schema: %w", err)
		}
	}
	return s, nil
}

// initIndex creates an ascending compound index on (conversation_id, seq, _id)
// so per-conversation reads sort efficiently and ties are deterministic.
func (s *Store) initIndex(ctx context.Context) error {
	_, err := s.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: fieldConversationID, Value: 1},
			{Key: fieldSequence, Value: 1},
			{Key: fieldID, Value: 1},
		},
		Options: options.Index().SetName("conversation_id_sequence_idx"),
	})
	return err
}

// Write inserts every message under conversationID via InsertMany. A reserved
// sequence range preserves argument order and remains monotonic if the local
// clock moves backward.
// InsertMany is ordered but is not a multi-document transaction: an error can
// leave earlier messages stored. The returned error preserves the driver cause.
func (s *Store) Write(ctx context.Context, conversationID history.ConversationID, messages ...chat.Message) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = conversationID.Validate(); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}

	encoded, err := encodeMessages(messages)
	if err != nil {
		return fmt.Errorf("mongodb: write: encode messages: %w", err)
	}
	now := time.Now().UTC()
	sequenceBase := s.sequence.Reserve(len(encoded))
	docs := make([]any, 0, len(encoded))
	for index, raw := range encoded {
		docs = append(docs, bson.M{
			fieldConversationID: conversationID.String(),
			fieldSequence:       sequenceBase + int64(index),
			fieldMessage:        string(raw),
			fieldCreatedAt:      now,
		})
	}

	result, err := s.collection.InsertMany(ctx, docs)
	if err != nil {
		return fmt.Errorf("mongodb: write: insert messages: %w", err)
	}
	if result == nil || !result.Acknowledged {
		return errUnacknowledged("write")
	}
	return nil
}

// errUnacknowledged reports a write concern under which no MongoDB write can be
// confirmed. Under w: 0 the server sends no reply, so the driver returns a nil
// error for a write it never heard about; its Acknowledged flag is the only
// part of the result that means anything. InsertManyResult.InsertedIDs cannot
// substitute for it — the driver fills that slice from the request before
// sending, so its length always matches the input.
func errUnacknowledged(operation string) error {
	return fmt.Errorf(
		"mongodb: %s: collection writes are unacknowledged (w: 0), so the operation cannot be confirmed",
		operation,
	)
}

// Read returns every message stored under conversationID in
// insertion order.
func (s *Store) Read(ctx context.Context, conversationID history.ConversationID) (storedMessages []chat.Message, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = conversationID.Validate(); err != nil {
		return nil, err
	}

	var cursor *mongo.Cursor
	cursor, err = s.collection.Find(ctx,
		bson.M{fieldConversationID: conversationID.String()},
		options.Find().SetSort(bson.D{
			{Key: fieldSequence, Value: 1},
			{Key: fieldID, Value: 1},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("mongodb: read: find messages: %w", err)
	}

	var documents []struct {
		ID       bson.ObjectID `bson:"_id"`
		Sequence int64         `bson:"seq"`
		Message  string        `bson:"message"`
	}
	if err = cursor.All(ctx, &documents); err != nil {
		return nil, fmt.Errorf("mongodb: read: decode documents: %w", err)
	}
	storedMessages = make([]chat.Message, 0, len(documents))
	for index, document := range documents {
		if document.ID.IsZero() {
			return nil, fmt.Errorf("mongodb: read: document %d is missing ID", index)
		}
		if document.Sequence <= 0 {
			return nil, fmt.Errorf("mongodb: read: document %s has invalid sequence %d", document.ID.Hex(), document.Sequence)
		}
		message, err := decodeMessage([]byte(document.Message))
		if err != nil {
			return nil, fmt.Errorf("mongodb: read: decode message %d: %w", index, err)
		}
		storedMessages = append(storedMessages, message)
	}
	return storedMessages, nil
}

// Clear drops every document for conversationID. Unknown ids result
// in a no-op (DeleteMany matches zero docs).
func (s *Store) Clear(ctx context.Context, conversationID history.ConversationID) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = conversationID.Validate(); err != nil {
		return err
	}

	result, err := s.collection.DeleteMany(ctx, bson.M{fieldConversationID: conversationID.String()})
	if err != nil {
		return fmt.Errorf("mongodb: clear: delete messages: %w", err)
	}
	if result == nil || !result.Acknowledged {
		return errUnacknowledged("clear")
	}
	return nil
}

// Conversations returns distinct conversation IDs in lexical order. It is a
// deliberate cross-conversation scan for operational tasks.
func (s *Store) Conversations(ctx context.Context) (ids []history.ConversationID, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}

	var storedIDs []string
	if err = s.collection.Distinct(ctx, fieldConversationID, bson.D{}).Decode(&storedIDs); err != nil {
		return nil, fmt.Errorf("mongodb: list conversations: query distinct IDs: %w", err)
	}
	ids = make([]history.ConversationID, 0, len(storedIDs))
	for _, id := range storedIDs {
		conversationID := history.ConversationID(id)
		if err := conversationID.Validate(); err != nil {
			return nil, fmt.Errorf("mongodb: list conversations: invalid stored ID %q: %w", id, err)
		}
		ids = append(ids, conversationID)
	}
	slices.Sort(ids)
	return ids, nil
}
