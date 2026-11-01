package opensearch

import (
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	DefaultMaxBulkBytes           = 5_000_000
	Provider                      = "OpenSearch"
	DefaultIndexName              = "scope-vector-index"
	DefaultMaxResponseBytes int64 = 16 << 20
	contentField                  = "content"
	embeddingField                = "embedding"
	metadataField                 = "metadata_json"
	nativeMaximumK                = 10000
	nativeMaximumIDBytes          = 512
	filterBatchSize               = 512
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
	if s.MaxBulkBytes < 0 {
		return errors.New("opensearch: negative MaxBulkBytes")
	}
	if s.IndexName != "" && (strings.TrimSpace(s.IndexName) != s.IndexName || strings.ContainsAny(s.IndexName, ",/*?\\")) {
		return errors.New("opensearch: IndexName must name one concrete index")
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes == math.MaxInt64 {
		return errors.New("opensearch: MaxResponseBytes must allow a bounded response")
	}
	return nil
}

var (
	ErrIndexMissing      = errors.New("opensearch: index not found")
	ErrIncompatibleIndex = errors.New("opensearch: native index is incompatible")
)
