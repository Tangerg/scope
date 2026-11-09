package elasticsearch

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
	Provider                      = "Elasticsearch"
	DefaultIndexName              = "scope-vector-index"
	DefaultMaxResponseBytes int64 = 16 << 20
	contentField                  = "content"
	embeddingField                = "embedding"
	metadataField                 = "metadata_json"
	nativeMaximumK                = 10000
	nativeMaximumIDBytes          = 512
	nativeMaximumDimensions       = 4096
	filterBatchSize               = 512
)

// APIClient borrows the native SDK's transport. The host owns authentication,
// topology, retries, timeouts and lifetime; Store closes every response body.
type APIClient interface {
	Perform(*http.Request) (*http.Response, error)
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
		return errors.New("elasticsearch: Client is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.MaxBulkBytes < 0 {
		return errors.New("elasticsearch: negative MaxBulkBytes")
	}
	if s.IndexName != "" && (strings.TrimSpace(s.IndexName) != s.IndexName || strings.ContainsAny(s.IndexName, ",/*?\\")) {
		return errors.New("elasticsearch: IndexName must name one concrete index")
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes == math.MaxInt64 {
		return errors.New("elasticsearch: MaxResponseBytes must allow a bounded response")
	}
	return nil
}

var (
	ErrIndexMissing      = errors.New("elasticsearch: index not found")
	ErrIncompatibleIndex = errors.New("elasticsearch: native index is incompatible")
)
