package elasticsearch

import (
	"context"
	"errors"
	"net/http"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic"
)

const (
	Provider                = "Elasticsearch"
	nativeMaximumDimensions = 4096
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
	config, err := s.store()
	if err != nil {
		return err
	}
	return config.Validate()
}

func (s StoreConfig) store() (elastic.StoreConfig, error) {
	if lo.IsNil(s.Client) {
		return elastic.StoreConfig{}, errors.New("elasticsearch: Client is required")
	}
	return elastic.StoreConfig{
		Engine:           engine{},
		Perform:          s.Client.Perform,
		IndexName:        s.IndexName,
		Space:            s.Space,
		MaxBulkBytes:     s.MaxBulkBytes,
		MaxResponseBytes: s.MaxResponseBytes,
	}, nil
}

// NewStore admits the host-provisioned index through the Elasticsearch engine.
func NewStore(ctx context.Context, config StoreConfig) (*elastic.Store, error) {
	store, err := config.store()
	if err != nil {
		return nil, err
	}
	return elastic.NewStore(ctx, store)
}
