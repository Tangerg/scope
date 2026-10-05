package pinecone

import (
	"context"
	"errors"
	"strings"

	pineconesdk "github.com/pinecone-io/go-pinecone/v4/pinecone"
	"github.com/samber/lo"
	"google.golang.org/grpc"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider             = "Pinecone"
	MaxTopK              = 10000
	MaxVectorsPerUpsert  = 1000
	contentField         = "content"
	metadataField        = "metadata_json"
	maximumIDsPerDelete  = 1000
	metadataListPageSize = uint32(100)
	filterGroupSize      = 100
)

// APIClient reads authoritative native index policy and opens a native data
// connection. The host owns credentials, transport policy and client lifetime;
// Store owns and closes the connection it opens.
type APIClient interface {
	DescribeIndex(context.Context, string) (*pineconesdk.Index, error)
	Index(pineconesdk.NewIndexConnParams, ...grpc.DialOption) (*pineconesdk.IndexConnection, error)
}

type StoreConfig struct {
	Client          APIClient
	IndexName       string
	Namespace       string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return ErrMissingClient
	}
	if s.IndexName == "" || strings.TrimSpace(s.IndexName) != s.IndexName {
		return ErrMissingIndexName
	}
	if err := validateNativeIdentifier(s.Namespace, true); err != nil {
		return err
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
	ErrMissingClient          = errors.New("pinecone: Client is required")
	ErrMissingIndexName       = errors.New("pinecone: IndexName is required")
	ErrMissingEmbeddingModel  = errors.New("pinecone: EmbeddingModel is required")
	ErrMissingDocumentBatcher = errors.New("pinecone: DocumentBatcher is required")
	ErrIncompatibleIndex      = errors.New("pinecone: native index is incompatible")
)
