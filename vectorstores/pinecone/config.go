package pinecone

import (
	"context"
	"errors"
	"strings"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"
	"github.com/samber/lo"
	"google.golang.org/grpc"

	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider            = "Pinecone"
	vectorField         = "_values"
	MaxTopK             = 10000
	contentField        = "content"
	metadataField       = "metadata_json"
	maximumIDsPerDelete = 1000

	termsField = "metadata_terms"
	// SpaceTag is the native index tag provisioning must bind to the encoder space.
	SpaceTag = "scope_vector_space"
)

// APIClient reads authoritative native index policy and opens a native data
// connection. The host owns credentials, transport policy and client lifetime;
// Store owns and closes the connection it opens.
type APIClient interface {
	DescribeIndex(context.Context, string) (*pineconesdk.Index, error)
	Index(pineconesdk.NewIndexConnParams, ...grpc.DialOption) (*pineconesdk.IndexConnection, error)
}

type StoreConfig struct {
	Client    APIClient
	IndexName string
	Namespace string
	Space     vectorstore.SpaceID
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
	return s.Space.Validate()
}

var (
	ErrMissingClient    = errors.New("pinecone: Client is required")
	ErrMissingIndexName = errors.New("pinecone: IndexName is required")

	ErrIncompatibleIndex = errors.New("pinecone: native index is incompatible")
)
