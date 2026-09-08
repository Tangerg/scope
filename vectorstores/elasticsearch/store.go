package elasticsearch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Provider is the stable backend name for host-side attribution.
const Provider = "Elasticsearch"

// Exported defaults keep constructor behavior visible and overridable.
const (
	DefaultIndexName          = "scope-vector-index"
	DefaultEmbeddingField     = "embedding"
	DefaultContentField       = "content"
	DefaultMetadataField      = "metadata"
	DefaultSimilarity         = SimilarityCosine
	defaultNumCandidatesMul   = 1.5 // num_candidates = ceil(topK * multiplier)
	mappingTypeText           = "text"
	mappingTypeDenseVector    = "dense_vector"
	mappingTypeObject         = "object"
	mappingTypeKeyword        = "keyword"
	maximumErrorResponseBytes = int64(64 * 1024)
)

type createIndexRequest struct {
	Mappings indexMappings `json:"mappings"`
}

type indexMappings struct {
	DynamicTemplates []map[string]dynamicTemplate `json:"dynamic_templates,omitempty"`
	Properties       map[string]any               `json:"properties"`
}

// dynamicTemplate names one dynamic-mapping rule. Metadata keys are unknown at
// index-creation time, so their fields have to be mapped dynamically; the
// default for a JSON string is "text with a .keyword sub-field", and the text
// field is analyzed. Filters compare whole values, so the metadata path maps
// strings straight to keyword instead, which also avoids the sub-field's
// ignore_above cutoff.
type dynamicTemplate struct {
	PathMatch        string         `json:"path_match"`
	MatchMappingType string         `json:"match_mapping_type"`
	Mapping          map[string]any `json:"mapping"`
}

// metadataKeywordTemplate keeps string metadata exactly comparable. Without it
// `metadata.author:"Alice"` reaches the analyzed text field and matches an
// author of "Alice Smith" or "alice", which is neither whole-value nor
// case-sensitive and disagrees with filter.Match.
func metadataKeywordTemplate(metadataField string) map[string]dynamicTemplate {
	return map[string]dynamicTemplate{
		"metadata_strings_are_keywords": {
			PathMatch:        metadataField + ".*",
			MatchMappingType: "string",
			Mapping:          map[string]any{"type": mappingTypeKeyword},
		},
	}
}

type textFieldMapping struct {
	Type string `json:"type"`
}

type vectorFieldMapping struct {
	Type       string `json:"type"`
	Dimensions int    `json:"dims"`
	Similarity string `json:"similarity"`
	Index      bool   `json:"index"`
}

type objectFieldMapping struct {
	Type    string `json:"type"`
	Dynamic bool   `json:"dynamic"`
}

// SimilarityFunction selects the Elasticsearch dense-vector similarity
// metric. The chosen value is recorded in the index mapping; changing
// it after the index is created has no effect.
type SimilarityFunction string

const (
	// SimilarityCosine — cosine similarity. Default; suitable for
	// most use cases.
	SimilarityCosine SimilarityFunction = "cosine"

	// SimilarityL2 — Euclidean (L2) distance.
	SimilarityL2 SimilarityFunction = "l2_norm"

	// SimilarityDotProduct — dot product. Recommended for
	// already-normalized embeddings (e.g. OpenAI's).
	SimilarityDotProduct SimilarityFunction = "dot_product"
)

func (s SimilarityFunction) Valid() bool {
	switch s {
	case SimilarityCosine, SimilarityL2, SimilarityDotProduct:
		return true
	default:
		return false
	}
}

func (s SimilarityFunction) String() string { return string(s) }

// StoreConfig contains configuration options for the Elasticsearch
// vector store.
type StoreConfig struct {
	// Client is the go-elasticsearch typed client. Required.
	Client *elasticsearch.Client

	// IndexName names the Elasticsearch index. Optional: defaults
	// to [DefaultIndexName].
	IndexName string

	// EmbeddingField is the dense_vector field name. Optional:
	// defaults to [DefaultEmbeddingField].
	EmbeddingField string

	// ContentField is the field that stores the document text.
	// Optional: defaults to [DefaultContentField].
	ContentField string

	// MetadataField is the object field that stores metadata.
	// Optional: defaults to [DefaultMetadataField]. It must differ from
	// ContentField and EmbeddingField.
	MetadataField string

	// EmbeddingModel produces vectors for the documents. Required.
	EmbeddingModel embedding.Model

	// DocumentBatcher batches documents before bulk upsert. Required.
	DocumentBatcher vectorstore.Batcher

	// Dimensions sets the dense_vector width for a newly created index. It
	// must be positive when creating an index; an existing index does not need it.
	Dimensions int

	// Similarity selects the similarity metric used at index time.
	// Optional: defaults to [SimilarityCosine].
	Similarity SimilarityFunction

	// InitializeSchema, when true, creates the index with the right
	// mapping if it doesn't already exist. When false and the index
	// is missing, [NewStore] returns [ErrIndexMissing].
	InitializeSchema bool

	// NumCandidatesMultiplier scales the KNN num_candidates parameter.
	// num_candidates = ceil(topK * multiplier). Higher = better
	// recall, slower. Optional: defaults to 1.5.
	NumCandidatesMultiplier float64
}

func (s StoreConfig) Validate() error {
	s.applyDefaults()
	if s.Client == nil {
		return errors.New("elasticsearch: Client is required")
	}
	if lo.IsNil(s.EmbeddingModel) {
		return errors.New("elasticsearch: EmbeddingModel is required")
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("elasticsearch: DocumentBatcher is required")
	}
	if s.Dimensions < 0 {
		return errors.New("elasticsearch: Dimensions must be >= 0")
	}
	if !s.Similarity.Valid() {
		return fmt.Errorf("elasticsearch: unsupported Similarity %q", s.Similarity)
	}
	if s.ContentField == s.EmbeddingField || s.ContentField == s.MetadataField || s.EmbeddingField == s.MetadataField {
		return errors.New("elasticsearch: ContentField, EmbeddingField, and MetadataField must be distinct")
	}
	return nil
}

// applyDefaults fills zero fields with documented defaults.
func (s *StoreConfig) applyDefaults() {
	s.IndexName = cmp.Or(s.IndexName, DefaultIndexName)
	s.EmbeddingField = cmp.Or(s.EmbeddingField, DefaultEmbeddingField)
	s.ContentField = cmp.Or(s.ContentField, DefaultContentField)
	s.MetadataField = cmp.Or(s.MetadataField, DefaultMetadataField)
	s.Similarity = cmp.Or(s.Similarity, DefaultSimilarity)
	if s.NumCandidatesMultiplier <= 0 {
		s.NumCandidatesMultiplier = defaultNumCandidatesMul
	}
}

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store is an Elasticsearch-backed implementation of
// the vectorstore capability interfaces. It uses the dense_vector field type and the
// `knn` query for similarity search.
type Store struct {
	client           *elasticsearch.Client
	indexName        string
	embeddingField   string
	contentField     string
	metadataField    string
	embeddingClient  embeddingclient.Client
	documentBatcher  vectorstore.Batcher
	dimensions       int
	similarity       SimilarityFunction
	numCandidatesMul float64
}

// NewStore performs schema setup during construction, which is why it takes
// a context: a store returned before its index mapping exists would fail on
// the first index rather than at wiring, where the misconfiguration actually
// is.
func NewStore(ctx context.Context, config StoreConfig) (*Store, error) {
	config.applyDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	embeddingClient, err := embeddingclient.New(config.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: create embedding client: %w", err)
	}

	store := &Store{
		client:           config.Client,
		indexName:        config.IndexName,
		embeddingField:   config.EmbeddingField,
		contentField:     config.ContentField,
		metadataField:    config.MetadataField,
		embeddingClient:  embeddingClient,
		documentBatcher:  config.DocumentBatcher,
		dimensions:       config.Dimensions,
		similarity:       config.Similarity,
		numCandidatesMul: config.NumCandidatesMultiplier,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("elasticsearch: initialize store: %w", err)
	}
	return store, nil
}

// initialize resolves dimensions and creates the index when requested.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	exists, err := s.indexExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if !initSchema {
		return errors.New("elasticsearch: index not found and InitializeSchema is false")
	}

	if s.dimensions <= 0 {
		return errors.New("elasticsearch: Dimensions must be > 0")
	}

	return s.createIndex(ctx)
}

func (s *Store) indexExists(ctx context.Context) (bool, error) {
	resp, err := s.client.Indices.Exists(
		[]string{s.indexName},
		s.client.Indices.Exists.WithContext(ctx),
	)
	if err != nil {
		return false, fmt.Errorf("elasticsearch: check index %q: %w", s.indexName, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		body, readErr := readErrorResponse(resp.Body)
		if readErr != nil {
			return false, fmt.Errorf("elasticsearch: read index existence error for %q with status %d: %w",
				s.indexName, resp.StatusCode, readErr)
		}
		return false, fmt.Errorf("elasticsearch: check index %q: status=%d body=%s",
			s.indexName, resp.StatusCode, string(body))
	}
}

func (s *Store) createIndex(ctx context.Context) error {
	properties := map[string]any{
		s.contentField: textFieldMapping{Type: mappingTypeText},
		s.embeddingField: vectorFieldMapping{
			Type:       mappingTypeDenseVector,
			Dimensions: s.dimensions,
			Similarity: string(s.similarity),
			Index:      true,
		},
		s.metadataField: objectFieldMapping{Type: mappingTypeObject, Dynamic: true},
	}
	body, err := encodeJSONRequest(createIndexRequest{
		Mappings: indexMappings{
			DynamicTemplates: []map[string]dynamicTemplate{
				metadataKeywordTemplate(s.metadataField),
			},
			Properties: properties,
		},
	})
	if err != nil {
		return err
	}

	resp, err := s.client.Indices.Create(
		s.indexName,
		s.client.Indices.Create.WithBody(body),
		s.client.Indices.Create.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("elasticsearch: create index %q: %w", s.indexName, err)
	}
	defer resp.Body.Close()
	if resp.IsError() {
		body, readErr := readErrorResponse(resp.Body)
		if readErr != nil {
			return fmt.Errorf("elasticsearch: read create-index error for %q with status %d: %w",
				s.indexName, resp.StatusCode, readErr)
		}
		return fmt.Errorf("elasticsearch: create index %q: status=%d body=%s",
			s.indexName, resp.StatusCode, string(body))
	}
	return nil
}

func readErrorResponse(reader io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, maximumErrorResponseBytes))
}

// Index embeds the documents and bulk-indexes them.
