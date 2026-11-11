package vespa

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/core/httpclient"
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
	RankingProfile string
	Space          vectorstore.SpaceID
	Dimensions     int
	// HTTPClient remains host-owned. Scope snapshots client settings and
	// restricts requests to Endpoint's origin while sharing its transport and jar.
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	endpoint, err := url.Parse(s.Endpoint)
	_, originErr := httpclient.NewOrigin(context.Background(), endpoint)
	if err != nil || originErr != nil || strings.ContainsAny(s.Endpoint, "?#") {
		return errors.New("vespa: Endpoint must be an absolute HTTP URL without credentials, query, or fragment")
	}
	if s.RankingProfile == "" {
		return errors.New("vespa: RankingProfile is required")
	}
	if err := s.Space.Validate(); err != nil {
		return err
	}
	if s.Dimensions < 1 {
		return errors.New("vespa: positive Dimensions required")
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
}

// DocumentNamespace binds a logical namespace to its coordinate space and
// width; changing the binding cannot access the previous document IDs.
func (s StoreConfig) DocumentNamespace() string {
	return cmp.Or(s.Namespace, s.SchemaName) + "_" + s.Space.StorageKey() + "_" + strconv.Itoa(s.Dimensions)
}
