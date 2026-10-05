package qdrant

import (
	"context"
	"errors"
	"strings"

	qdrantclient "github.com/qdrant/go-client/qdrant"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider       = "Qdrant"
	contentField   = "content"
	metadataField  = "metadata_json"
	filterPageSize = 256
)

// APIClient exposes the native operations Store consumes. The host owns the
// client, authentication, transport and collection provisioning.
type APIClient interface {
	ListCollections(context.Context) ([]string, error)
	GetCollectionInfo(context.Context, string) (*qdrantclient.CollectionInfo, error)
	Upsert(context.Context, *qdrantclient.UpsertPoints) (*qdrantclient.UpdateResult, error)
	Query(context.Context, *qdrantclient.QueryPoints) ([]*qdrantclient.ScoredPoint, error)
	ScrollAndOffset(context.Context, *qdrantclient.ScrollPoints) ([]*qdrantclient.RetrievedPoint, *qdrantclient.PointId, error)
	Delete(context.Context, *qdrantclient.DeletePoints) (*qdrantclient.UpdateResult, error)
}

type StoreConfig struct {
	Client          APIClient
	CollectionName  string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return ErrMissingClient
	}
	if s.CollectionName == "" || strings.TrimSpace(s.CollectionName) != s.CollectionName {
		return ErrMissingCollectionName
	}
	if lo.IsNil(s.EmbeddingModel) {
		return ErrMissingEmbeddingModel
	}
	if lo.IsNil(s.DocumentBatcher) {
		return ErrMissingDocumentBatcher
	}
	return nil
}

var (
	ErrMissingClient          = errors.New("qdrant: Client is required")
	ErrMissingCollectionName  = errors.New("qdrant: CollectionName is required")
	ErrMissingEmbeddingModel  = errors.New("qdrant: EmbeddingModel is required")
	ErrMissingDocumentBatcher = errors.New("qdrant: DocumentBatcher is required")
	ErrInvalidPointID         = errors.New("qdrant: invalid point ID")
	ErrIncompatibleCollection = errors.New("qdrant: incompatible native collection")
)
