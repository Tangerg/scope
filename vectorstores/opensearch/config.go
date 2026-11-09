package opensearch

import (
	"cmp"
	"errors"
	"net/http"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic"
)

const (
	Provider                = "OpenSearch"
	contentField            = "content"
	embeddingField          = "embedding"
	metadataField           = "metadata_json"
	nativeMaximumK          = 10000
	nativeMaximumDimensions = 16000
	filterBatchSize         = 512
)

// APIClient borrows the native SDK's streaming transport. The host owns authentication,
// topology, retries, timeouts and lifetime; Store closes every response body.
type APIClient interface {
	Stream(*http.Request) (*http.Response, error)
}

type StoreConfig struct {
	Client    APIClient
	IndexName string
	Space     vectorstore.SpaceID
	// MaxBulkBytes limits the encoded action/document lines per native write.
	MaxBulkBytes     int
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("opensearch: Client is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if err := (elastic.ClientConfig{Perform: s.Client.Stream, MaxResponseBytes: s.MaxResponseBytes}).Validate(); err != nil {
		return err
	}
	return (elastic.BulkConfig{Index: cmp.Or(s.IndexName, elastic.DefaultIndexName), Operation: elastic.Index, MaxBytes: s.MaxBulkBytes}).Validate()
}

var (
	ErrIndexMissing      = errors.New("opensearch: index not found")
	ErrIncompatibleIndex = errors.New("opensearch: native index is incompatible")
)
