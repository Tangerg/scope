package elasticsearch

import (
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider                      = "Elasticsearch"
	DefaultIndexName              = "scope-vector-index"
	DefaultMaxResponseBytes int64 = 16 << 20
	contentField                  = "content"
	embeddingField                = "embedding"
	metadataField                 = "metadata_json"
	nativeMaximumK                = 10000
	nativeMaximumIDBytes          = 512
	filterBatchSize               = 512
)

// APIClient borrows the native SDK's transport. The host owns authentication,
// topology, retries, timeouts and lifetime; Store closes every response body.
type APIClient interface {
	Perform(*http.Request) (*http.Response, error)
}

type StoreConfig struct {
	Client           APIClient
	IndexName        string
	EmbeddingModel   embedding.Model
	DocumentBatcher  vectorstore.Batcher
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	if lo.IsNil(s.Client) {
		return errors.New("elasticsearch: Client is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("elasticsearch: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("elasticsearch: DocumentBatcher is required")
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
