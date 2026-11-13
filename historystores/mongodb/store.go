package mongodb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/samber/lo"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
)

const (
	fieldID             = "_id"
	fieldConversationID = "conversation_id"
	fieldKind           = "kind"
	fieldSequence       = "sequence"
	fieldMessages       = "messages"
	fieldClosed         = "closed"
	fieldVersion        = "version"
	fieldNextSequence   = "next_sequence"
	kindConversation    = "conversation"
	kindAppend          = "append"
	operatorSet         = "$set"
	operatorSetOnInsert = "$setOnInsert"
	operatorIncrement   = "$inc"
	operatorExists      = "$exists"
	operatorMatch       = "$match"
	operatorGroup       = "$group"
	simpleCollation     = "simple"
)

// MessageCollection is the native surface used within one collection-bound
// session. Database identifies the same client that executes these operations.
type MessageCollection interface {
	Database() *mongo.Database
	FindOne(context.Context, any, ...options.Lister[options.FindOneOptions]) *mongo.SingleResult
	FindOneAndUpdate(context.Context, any, any, ...options.Lister[options.FindOneAndUpdateOptions]) *mongo.SingleResult
	Find(context.Context, any, ...options.Lister[options.FindOptions]) (*mongo.Cursor, error)
	UpdateOne(context.Context, any, any, ...options.Lister[options.UpdateOneOptions]) (*mongo.UpdateResult, error)
	InsertOne(context.Context, any, ...options.Lister[options.InsertOneOptions]) (*mongo.InsertOneResult, error)
	DeleteMany(context.Context, any, ...options.Lister[options.DeleteManyOptions]) (*mongo.DeleteResult, error)
	Aggregate(context.Context, any, ...options.Lister[options.AggregateOptions]) (*mongo.Cursor, error)
	Indexes() mongo.IndexView
}

type StoreConfig struct {
	Collection       MessageCollection
	InitializeSchema bool
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Collection) {
		return errors.New("mongodb: collection is required")
	}
	return nil
}

type Store struct {
	collection MessageCollection
	transact   func(context.Context, func(context.Context) error) error
}

var (
	_ MessageCollection = (*mongo.Collection)(nil)
	_ history.Store     = (*Store)(nil)
	_ history.Lister    = (*Store)(nil)
)

func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	database := config.Collection.Database()
	if database == nil {
		return nil, errors.New("mongodb: collection database is required")
	}
	store := &Store{collection: config.Collection}
	client := database.Client()
	if client == nil {
		return nil, errors.New("mongodb: collection client is required")
	}
	store.transact = func(ctx context.Context, apply func(context.Context) error) error {
		session, err := client.StartSession()
		if err != nil {
			return err
		}
		defer session.EndSession(ctx)
		_, err = session.WithTransaction(ctx, func(ctx context.Context) (any, error) { return nil, apply(ctx) },
			options.Transaction().SetReadConcern(readconcern.Snapshot()).SetWriteConcern(writeconcern.Majority()).SetReadPreference(readpref.Primary()))
		return err
	}
	if config.InitializeSchema {
		_, err := store.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: fieldConversationID, Value: 1}, {Key: fieldKind, Value: 1}, {Key: fieldSequence, Value: 1}},
			Options: options.Index().SetCollation(&options.Collation{Locale: simpleCollation}),
		})
		if err != nil {
			return nil, fmt.Errorf("mongodb: initialize schema: %w", err)
		}
	}
	return store, nil
}

// The immutable gate identity makes concurrent initialization harmless and can
// never reopen a closed conversation. No message is published by this step.
func (s *Store) ensureGate(ctx context.Context, id history.ConversationID) error {
	result, err := s.collection.UpdateOne(ctx, bson.D{{Key: fieldID, Value: gateID(id)}}, bson.D{{Key: operatorSetOnInsert, Value: bson.D{{Key: fieldConversationID, Value: id.String()}, {Key: fieldKind, Value: kindConversation}, {Key: fieldClosed, Value: false}, {Key: fieldVersion, Value: int64(0)}, {Key: fieldNextSequence, Value: int64(0)}}}}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return err
	}
	if result == nil || !result.Acknowledged {
		return errUnacknowledged("initialize conversation")
	}
	return nil
}

// Snapshot reads alone can be stale. Every operation writes the gate, so
// append, read and closure share the same serialized conversation boundary.
func (s *Store) lockGate(ctx context.Context, id history.ConversationID) (conversationGate, error) {
	var gate conversationGate
	err := s.collection.FindOneAndUpdate(ctx, bson.D{{Key: fieldID, Value: gateID(id)}}, bson.D{{Key: operatorIncrement, Value: bson.D{{Key: fieldVersion, Value: 1}}}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&gate)
	return gate, err
}

func (s *Store) Append(ctx context.Context, batch history.Batch) (history.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return history.Receipt{}, err
	}
	if err := batch.Validate(); err != nil {
		return history.Receipt{}, err
	}
	if err := s.ensureGate(ctx, batch.ConversationID()); err != nil {
		return history.Receipt{}, fmt.Errorf("mongodb: append gate: %w", err)
	}
	err := s.transact(ctx, func(ctx context.Context) error {
		gate, err := s.lockGate(ctx, batch.ConversationID())
		if err != nil {
			return err
		}
		if gate.Closed {
			return history.ErrConversationClosed
		}
		var existing appendRecord
		err = s.collection.FindOne(ctx, bson.D{{Key: fieldID, Value: batchID(batch)}}).Decode(&existing)
		if err == nil {
			if existing.Digest != batch.Digest() {
				return history.ErrAppendConflict
			}
			return nil
		}
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return err
		}
		if gate.NextSequence == math.MaxInt64 {
			return errors.New("mongodb: conversation sequence exhausted")
		}
		record := appendRecord{ID: batchID(batch), ConversationID: batch.ConversationID().String(), Kind: kindAppend, AppendID: batch.ID().String(), Digest: batch.Digest(), Sequence: gate.NextSequence + 1, Messages: string(batch.EncodedMessages())}
		result, err := s.collection.InsertOne(ctx, record)
		if err != nil {
			return err
		}
		if result == nil || !result.Acknowledged {
			return errUnacknowledged("append")
		}
		updated, err := s.collection.UpdateOne(ctx, bson.D{{Key: fieldID, Value: gateID(batch.ConversationID())}}, bson.D{{Key: operatorSet, Value: bson.D{{Key: fieldNextSequence, Value: record.Sequence}}}})
		if err != nil {
			return err
		}
		if updated == nil || !updated.Acknowledged || updated.MatchedCount != 1 {
			return errUnacknowledged("advance conversation sequence")
		}
		return nil
	})
	if err != nil {
		return history.Receipt{}, fmt.Errorf("mongodb: append transaction: %w", err)
	}
	return batch.Receipt(), nil
}

func (s *Store) Read(ctx context.Context, id history.ConversationID) ([]chat.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	messages := []chat.Message{}
	err := s.transact(ctx, func(ctx context.Context) error {
		messages = []chat.Message{}
		gate, err := s.lockGate(ctx, id)
		if errors.Is(err, mongo.ErrNoDocuments) || err == nil && gate.Closed {
			return nil
		}
		if err != nil {
			return err
		}
		cursor, err := s.collection.Find(ctx, bson.D{{Key: fieldConversationID, Value: id.String()}, {Key: fieldKind, Value: kindAppend}}, options.Find().SetSort(bson.D{{Key: fieldSequence, Value: 1}}).SetCollation(&options.Collation{Locale: simpleCollation}))
		if err != nil {
			return err
		}
		var records []appendRecord
		if err := cursor.All(ctx, &records); err != nil {
			return err
		}
		for _, record := range records {
			if record.Sequence <= 0 || record.Messages == "" {
				return errors.New("mongodb: invalid stored append record")
			}
			batch, err := history.DecodeMessages([]byte(record.Messages))
			if err != nil {
				return err
			}
			messages = append(messages, batch...)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mongodb: read transaction: %w", err)
	}
	return messages, nil
}

func (s *Store) Clear(ctx context.Context, id history.ConversationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := id.Validate(); err != nil {
		return err
	}
	if err := s.ensureGate(ctx, id); err != nil {
		return err
	}
	err := s.transact(ctx, func(ctx context.Context) error {
		if _, err := s.lockGate(ctx, id); err != nil {
			return err
		}
		updated, err := s.collection.UpdateOne(ctx, bson.D{{Key: fieldID, Value: gateID(id)}}, bson.D{{Key: operatorSet, Value: bson.D{{Key: fieldClosed, Value: true}}}})
		if err != nil {
			return err
		}
		if updated == nil || !updated.Acknowledged || updated.MatchedCount != 1 {
			return errUnacknowledged("close conversation")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("mongodb: close conversation: %w", err)
	}
	// Cleanup failure must not roll back permanent closure. A separate majority
	// transaction can be retried without reopening the gate or publishing messages.
	return s.transact(ctx, func(ctx context.Context) error {
		deleted, err := s.collection.DeleteMany(ctx, bson.D{{Key: fieldConversationID, Value: id.String()}, {Key: fieldKind, Value: kindAppend}}, options.DeleteMany().SetCollation(&options.Collation{Locale: simpleCollation}))
		if err != nil {
			return err
		}
		if deleted == nil || !deleted.Acknowledged {
			return errUnacknowledged("clear messages")
		}
		return nil
	})
}

func (s *Store) Conversations(ctx context.Context) ([]history.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var groups []conversationGroup
	err := s.transact(ctx, func(ctx context.Context) error {
		groups = nil
		pipeline := mongo.Pipeline{
			bson.D{{Key: operatorMatch, Value: bson.D{{Key: fieldKind, Value: kindAppend}, {Key: fieldMessages, Value: bson.D{{Key: operatorExists, Value: true}}}}}},
			bson.D{{Key: operatorGroup, Value: bson.D{{Key: fieldID, Value: "$" + fieldConversationID}}}},
		}
		cursor, err := s.collection.Aggregate(ctx, pipeline, options.Aggregate().SetCollation(&options.Collation{Locale: simpleCollation}))
		if err != nil {
			return err
		}
		return cursor.All(ctx, &groups)
	})
	if err != nil {
		return nil, fmt.Errorf("mongodb: list snapshot: %w", err)
	}
	slices.SortFunc(groups, func(left, right conversationGroup) int { return strings.Compare(left.ID, right.ID) })
	ids := []history.ConversationID{}
	for _, group := range groups {
		id := history.ConversationID(group.ID)
		if validationErr := id.Validate(); validationErr != nil {
			return nil, validationErr
		}
		visible := false
		// Listing has no global snapshot contract. Release each gate before the
		// next one so an inventory cannot hold every conversation's write lock.
		err = s.transact(ctx, func(ctx context.Context) error {
			gate, lockErr := s.lockGate(ctx, id)
			if lockErr != nil {
				return lockErr
			}
			visible = !gate.Closed
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("mongodb: list gate: %w", err)
		}
		if visible {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func errUnacknowledged(operation string) error {
	return fmt.Errorf("mongodb: %s: write was not acknowledged", operation)
}

// Binary primary keys preserve exact identity even on collections with a
// language collation. Text queries and grouping explicitly use simple collation.
func gateID(id history.ConversationID) bson.Binary {
	return bson.Binary{Data: []byte(kindConversation + ":" + base64.RawURLEncoding.EncodeToString([]byte(id)))}
}

func batchID(batch history.Batch) bson.Binary {
	return bson.Binary{Data: []byte(kindAppend + ":" + base64.RawURLEncoding.EncodeToString([]byte(batch.ConversationID())) + ":" + base64.RawURLEncoding.EncodeToString([]byte(batch.ID())))}
}

type conversationGate struct {
	ID             bson.Binary `bson:"_id"`
	ConversationID string      `bson:"conversation_id"`
	Kind           string      `bson:"kind"`
	Closed         bool        `bson:"closed"`
	Version        int64       `bson:"version"`
	NextSequence   int64       `bson:"next_sequence"`
}

type appendRecord struct {
	ID             bson.Binary `bson:"_id"`
	ConversationID string      `bson:"conversation_id"`
	Kind           string      `bson:"kind"`
	AppendID       string      `bson:"append_id"`
	Digest         string      `bson:"digest"`
	Sequence       int64       `bson:"sequence"`
	Messages       string      `bson:"messages"`
}

type conversationGroup struct {
	ID string `bson:"_id"`
}
