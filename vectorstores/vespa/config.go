package vespa

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider                = "Vespa"
	DefaultContentField     = "content"
	DefaultEmbeddingField   = "embedding"
	DefaultQueryTensorName  = "q"
	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)
)

// StoreConfig selects an externally deployed application and host-owned transport.
type StoreConfig struct {
	Endpoint        string
	SchemaName      string
	Namespace       string
	ContentField    string
	EmbeddingField  string
	QueryTensorName string
	// RankingProfile must rank only by closeness(field, EmbeddingField).
	RankingProfile  string
	EmbeddingModel  embedding.Model
	DocumentBatcher vectorstore.Batcher
	// HTTPClient owns authentication, transport policy, and connection lifetime.
	HTTPClient *http.Client
	// MaxResponseBytes bounds each response; zero uses DefaultMaxResponseBytes.
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("vespa: Endpoint must be an absolute HTTP URL without credentials, query, or fragment")
	}
	if s.RankingProfile == "" {
		return errors.New("vespa: RankingProfile is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("vespa: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("vespa: DocumentBatcher is required")
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes == math.MaxInt64 {
		return errors.New("vespa: MaxResponseBytes must fit a positive bounded reader")
	}
	if err := (schemaFields{content: s.ContentField, embedding: s.EmbeddingField}).validate(); err != nil {
		return err
	}
	for name, value := range map[string]string{"SchemaName": s.SchemaName, "Namespace": s.Namespace, "QueryTensorName": s.QueryTensorName, "RankingProfile": s.RankingProfile} {
		if err := identifier(value).validate(name); err != nil {
			return fmt.Errorf("vespa: config: %w", err)
		}
	}
	return nil
}

func (s *StoreConfig) applyDefaults() {
	s.Namespace = cmp.Or(s.Namespace, s.SchemaName)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.EmbeddingField = cmp.Or(s.EmbeddingField, DefaultEmbeddingField)
	s.QueryTensorName = cmp.Or(s.QueryTensorName, DefaultQueryTensorName)
	if s.HTTPClient == nil {
		s.HTTPClient = http.DefaultClient
	}
}
