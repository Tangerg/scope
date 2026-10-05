package mongodb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider               = "MongoDB"
	DefaultVectorIndexName = "vector_index"
	DefaultNumCandidates   = 200
	MaxNumCandidates       = 10000
	idField                = "_id"
	contentField           = "content"
	metadataField          = "metadata_json"
	embeddingField         = "embedding"
	scoreField             = "score"
	filterPageSize         = 128
)

// DocumentCollection contains native data-plane operations. The host owns the
// collection, native client, credentials, index provisioning and lifecycle.
type DocumentCollection interface {
	BulkWrite(context.Context, []mongo.WriteModel, ...options.Lister[options.BulkWriteOptions]) (*mongo.BulkWriteResult, error)
	Aggregate(context.Context, any, ...options.Lister[options.AggregateOptions]) (*mongo.Cursor, error)
	DeleteMany(context.Context, any, ...options.Lister[options.DeleteManyOptions]) (*mongo.DeleteResult, error)
}

type StoreConfig struct {
	Collection      DocumentCollection
	VectorIndexName string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
	// NumCandidates is the native ANN recall floor, raised to cover TopK.
	NumCandidates int
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Collection) {
		return errors.New("mongodb: Collection is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("mongodb: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("mongodb: DocumentBatcher is required")
	}
	if strings.TrimSpace(s.VectorIndexName) != s.VectorIndexName {
		return errors.New("mongodb: VectorIndexName must not have surrounding whitespace")
	}
	if s.NumCandidates < 0 || s.NumCandidates > MaxNumCandidates {
		return fmt.Errorf("mongodb: NumCandidates must be in [0, %d]", MaxNumCandidates)
	}
	return nil
}

func (s *StoreConfig) applyDefaults() {
	s.VectorIndexName = cmp.Or(s.VectorIndexName, DefaultVectorIndexName)
	if s.NumCandidates == 0 {
		s.NumCandidates = DefaultNumCandidates
	}
}
