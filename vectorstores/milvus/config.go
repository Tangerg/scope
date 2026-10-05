package milvus

import (
	"errors"
	"regexp"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider              = "Milvus"
	fieldID               = "id"
	fieldVector           = "vector"
	fieldContent          = "content"
	fieldMeta             = "metadata"
	nativeMaxVarCharBytes = 65535
	sourcePageSize        = 16
	identityGroupSize     = 128
	nativeMaxTopK         = 16384
)

var collectionNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)

type StoreConfig struct {
	// Client owns authentication, transport, retries and lifetime.
	Client          *milvusclient.Client
	CollectionName  string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
}

func (s StoreConfig) Validate() error {
	if s.Client == nil {
		return ErrMissingClient
	}
	if s.CollectionName == "" {
		return ErrMissingCollectionName
	}
	if !collectionNamePattern.MatchString(s.CollectionName) {
		return errors.New("milvus: invalid CollectionName")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return ErrMissingEmbeddingModel
	}
	if lo.IsNil(s.DocumentBatcher) {
		return ErrMissingDocumentBatcher
	}
	return nil
}
