package elasticsearch

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdmath "math"
	"net/http"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
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

	// elementTypeBit is the one dense_vector element type whose similarity
	// default differs, so it is the only one this store has to name.
	elementTypeBit = "bit"
)

var (
	// ErrIndexMissing reports an absent index that this store was not asked to
	// create.
	ErrIndexMissing = errors.New("elasticsearch: index not found")

	// ErrIncompatibleIndex reports an existing index whose vector field cannot
	// serve this store: it is absent, not a dense_vector, unindexed, or built
	// for a different similarity metric or width.
	ErrIncompatibleIndex = errors.New("elasticsearch: index is incompatible")
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
	// is missing, [NewStore] returns [ErrIndexMissing]. Either way an index
	// that already exists is checked against these settings and refused with
	// [ErrIncompatibleIndex] when it disagrees.
	InitializeSchema bool

	// NumCandidatesMultiplier scales the KNN num_candidates parameter.
	// num_candidates = ceil(topK * multiplier). Higher = better
	// recall, slower. Optional: defaults to 1.5. Values below 1 are
	// refused because Elasticsearch requires num_candidates to be at
	// least k, so such a store could not serve any search.
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
	// "[num_candidates] cannot be less than [k]", and num_candidates is
	// derived from k by this multiplier alone, so anything under 1 rejects
	// every search this store would ever send.
	if s.NumCandidatesMultiplier < 1 {
		return fmt.Errorf("elasticsearch: NumCandidatesMultiplier %g would ask for fewer candidates than results, which Elasticsearch rejects",
			s.NumCandidatesMultiplier)
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

// initialize creates the index when requested, and confirms an existing one
// agrees with the settings this store would have created it with.
func (s *Store) initialize(ctx context.Context, initSchema bool) error {
	exists, err := s.indexExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return s.verifyVectorField(ctx)
	}
	if !initSchema {
		return fmt.Errorf("%w: %q, and InitializeSchema is false", ErrIndexMissing, s.indexName)
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

// storedVectorField is the part of an existing dense_vector mapping this store
// has to agree with. Every attribute is a pointer or defaulted string because
// Elasticsearch omits what was left at its default rather than echoing it.
type storedVectorField struct {
	Type        string `json:"type"`
	Dimensions  *int   `json:"dims"`
	Similarity  string `json:"similarity"`
	ElementType string `json:"element_type"`
	Index       *bool  `json:"index"`
}

// verifyVectorField refuses an existing index whose vector field cannot serve
// this store.
//
// Similarity is the one that fails quietly. Elasticsearch derives _score from
// the field's own metric -- "(1 + cosine(query, vector)) / 2" is not
// "1 / (1 + l2_norm(query, vector)^2)" -- so a store configured for one metric
// against a field built for another returns plausible scores in the wrong
// scale, with MinScore filtering by a threshold that means something else.
// max_inner_product is worse: its score is "max_inner_product(query, vector)
// + 1", which is unbounded above, so Core's range would flatten every strong
// match onto the same value. This store's vocabulary cannot name that metric,
// so the comparison rejects it along with any other disagreement.
//
// index:false is the other refusal. It "defaults to true", and with it off
// "you can only use exact brute-force search" -- the knn query every Search
// sends is not available.
//
// None of it can be repaired in place: neither similarity nor dims can be
// changed after the field is created, so construction is the only useful place
// to say so.
// The return is named so the deferred body close can report a failure that
// would otherwise be dropped, the same way parseDeleteByQueryResponse does.
func (s *Store) verifyVectorField(ctx context.Context) (err error) {
	response, err := s.client.Indices.GetMapping(
		s.client.Indices.GetMapping.WithIndex(s.indexName),
		s.client.Indices.GetMapping.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("elasticsearch: read mapping for %q: %w", s.indexName, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if response.IsError() {
		body, readErr := readErrorResponse(response.Body)
		if readErr != nil {
			return fmt.Errorf("elasticsearch: read mapping error for %q with status %d: %w",
				s.indexName, response.StatusCode, readErr)
		}
		return fmt.Errorf("elasticsearch: read mapping for %q: status=%d body=%s",
			s.indexName, response.StatusCode, string(body))
	}

	// The response is keyed by resolved index name, which differs from the
	// configured one whenever it names an alias, so the single entry is read
	// rather than looked up.
	var mappings map[string]struct {
		Mappings struct {
			Properties map[string]storedVectorField `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&mappings); err != nil {
		return fmt.Errorf("elasticsearch: decode mapping for %q: %w", s.indexName, err)
	}
	if len(mappings) != 1 {
		return fmt.Errorf("elasticsearch: mapping for %q resolved to %d indices; point the store at one",
			s.indexName, len(mappings))
	}
	for _, mapping := range mappings {
		return s.validateVectorField(mapping.Mappings.Properties[s.embeddingField])
	}
	return nil
}

func (s *Store) validateVectorField(field storedVectorField) error {
	if field.Type == "" {
		return fmt.Errorf("%w: index %q declares no field named %q",
			ErrIncompatibleIndex, s.indexName, s.embeddingField)
	}
	if field.Type != mappingTypeDenseVector {
		return fmt.Errorf("%w: field %q has type %q, want %q",
			ErrIncompatibleIndex, s.embeddingField, field.Type, mappingTypeDenseVector)
	}
	if field.Index != nil && !*field.Index {
		return fmt.Errorf("%w: field %q is mapped with index:false, which serves only exact search rather than the knn query",
			ErrIncompatibleIndex, s.embeddingField)
	}
	if similarity := field.effectiveSimilarity(); similarity != s.similarity {
		return fmt.Errorf("%w: field %q is built for similarity %q, but the store is configured for %q, and _score means something different under each",
			ErrIncompatibleIndex, s.embeddingField, similarity, s.similarity)
	}
	if s.dimensions > 0 && field.Dimensions != nil && *field.Dimensions != s.dimensions {
		return fmt.Errorf("%w: field %q holds %d dimensions, but the store is configured for %d",
			ErrIncompatibleIndex, s.embeddingField, *field.Dimensions, s.dimensions)
	}
	return nil
}

// effectiveSimilarity resolves what Elasticsearch left out. Similarity
// "defaults to l2_norm when element_type: bit, otherwise it defaults to
// cosine", and element_type itself defaults to float.
func (s storedVectorField) effectiveSimilarity() SimilarityFunction {
	if s.Similarity != "" {
		return SimilarityFunction(s.Similarity)
	}
	if s.ElementType == elementTypeBit {
		return SimilarityL2
	}
	return SimilarityCosine
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

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("elasticsearch.Store.Index: %w", validateErr)
	}
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("elasticsearch.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("elasticsearch: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("elasticsearch: embed documents: %w", err)
		}

		var body bytes.Buffer
		for index, doc := range docs {
			id := doc.ID

			actionLine, encErr := json.Marshal(bulkAction{
				Index: &bulkActionTarget{Index: s.indexName, ID: id},
			})
			if encErr != nil {
				return fmt.Errorf("elasticsearch: encode bulk action: %w", encErr)
			}

			docBody := map[string]any{
				s.contentField:   doc.Text,
				s.embeddingField: embedding.Float32Vector(vectors[index]),
				s.metadataField:  doc.Metadata,
			}
			docLine, encErr := json.Marshal(docBody)
			if encErr != nil {
				return fmt.Errorf("elasticsearch: encode bulk doc: %w", encErr)
			}

			body.Write(actionLine)
			body.WriteByte(bulkRecordSeparator)
			body.Write(docLine)
			body.WriteByte(bulkRecordSeparator)
		}

		resp, err := s.client.Bulk(
			bytes.NewReader(body.Bytes()),
			s.client.Bulk.WithContext(ctx),
		)
		if err != nil {
			return fmt.Errorf("elasticsearch: bulk: %w", err)
		}
		if err = parseBulkResponse(resp, bulkOperationIndex); err != nil {
			return err
		}
	}
	return nil
}

// Search runs a KNN search over the embedding field. Optional
// metadata filtering is expressed via a query_string clause.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("elasticsearch.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("elasticsearch.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	knn := nearestNeighborQuery{
		Field:         s.embeddingField,
		QueryVector:   queryVec,
		K:             req.Options.ResultLimit(),
		NumCandidates: int(stdmath.Ceil(float64(req.Options.ResultLimit()) * s.numCandidatesMul)),
	}

	filterQuery, err := s.buildFilterQuery(req.Options.Filter)
	if err != nil {
		return nil, err
	}
	if filterQuery != "" {
		knn.Filter = &queryClause{QueryString: queryString{Query: filterQuery}}
	}

	body, err := encodeJSONRequest(searchRequest{Size: req.Options.ResultLimit(), KNN: knn})
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Search(
		s.client.Search.WithContext(ctx),
		s.client.Search.WithIndex(s.indexName),
		s.client.Search.WithBody(body),
	)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: search %s: %w", s.indexName, err)
	}
	defer resp.Body.Close()
	if resp.IsError() {
		body, readErr := readErrorResponse(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("elasticsearch: read search error response for %s with status %d: %w",
				s.indexName, resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("elasticsearch: search %s: status=%d body=%s",
			s.indexName, resp.StatusCode, string(body))
	}

	var parsed searchResponse
	if err = json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("elasticsearch: decode search response: %w", err)
	}
	if err := s.checkSearchCompleteness(parsed); err != nil {
		return nil, err
	}

	docs = make([]*vectorstore.SearchResult, 0, len(parsed.Hits.Hits))
	for _, hit := range parsed.Hits.Hits {
		score := s.normalizeScore(hit.Score)
		if score < req.Options.MinScore {
			continue
		}
		doc, err := s.toDocument(hit)
		if err != nil {
			return nil, err
		}
		docs = append(docs, &vectorstore.SearchResult{Document: doc, Score: score})
	}
	return &vectorstore.SearchResponse{Results: docs}, nil
}

// DeleteWhere removes every matching document with a single delete_by_query.
// Elasticsearch reports per-document failures, version conflicts, and query
// timeouts inside a successful response, so the store treats an incomplete
// deletion as an error. Documents already deleted stay deleted; the caller
// repeats the operation to converge. Implements [vectorstore.FilterDeleter].
func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("elasticsearch.Store.DeleteWhere: %w", err)
	}

	var filterQuery string
	filterQuery, err = s.buildFilterQuery(expr)
	if err != nil {
		return err
	}
	if filterQuery == "" {
		return errors.New("elasticsearch: refusing to delete on empty filter")
	}

	body, err := encodeJSONRequest(deleteByQueryRequest{
		Query: queryClause{QueryString: queryString{Query: filterQuery}},
	})
	if err != nil {
		return err
	}

	resp, err := s.client.DeleteByQuery(
		[]string{s.indexName},
		body,
		s.client.DeleteByQuery.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("elasticsearch: delete_by_query %s: %w", s.indexName, err)
	}
	return s.parseDeleteByQueryResponse(resp)
}

func (s *Store) parseDeleteByQueryResponse(response *esapi.Response) (err error) {
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("elasticsearch: close delete_by_query response: %w", closeErr))
		}
	}()
	if response.IsError() {
		body, readErr := readErrorResponse(response.Body)
		if readErr != nil {
			return fmt.Errorf("elasticsearch: read delete_by_query error response for %s with status %d: %w",
				s.indexName, response.StatusCode, readErr)
		}
		return fmt.Errorf("elasticsearch: delete_by_query %s: status=%d body=%s",
			s.indexName, response.StatusCode, string(body))
	}

	var parsed deleteByQueryResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("elasticsearch: decode delete_by_query response for %s: %w", s.indexName, err)
	}
	if failure := parsed.firstFailure(); failure != nil {
		reason := failure.Cause.Reason
		if reason == "" {
			reason = "provider returned no reason"
		}
		return fmt.Errorf("elasticsearch: delete_by_query %s failed for document %q with status %d: %s",
			s.indexName, failure.ID, failure.Status, reason)
	}
	if parsed.VersionConflicts != 0 {
		return fmt.Errorf("elasticsearch: delete_by_query %s left %d document(s) on version conflict",
			s.indexName, parsed.VersionConflicts)
	}
	if parsed.TimedOut {
		return fmt.Errorf("elasticsearch: delete_by_query %s timed out after deleting %d of %d document(s)",
			s.indexName, parsed.Deleted, parsed.Total)
	}
	if parsed.Deleted != parsed.Total {
		return fmt.Errorf("elasticsearch: delete_by_query %s deleted %d of %d matched document(s)",
			s.indexName, parsed.Deleted, parsed.Total)
	}
	return nil
}

// DeleteIDs removes documents by their _id via a single bulk request
// carrying one delete action per id. An empty slice is a no-op; unknown
// ids are silently ignored (the bulk delete reports `not_found` rather
// than an error). Implements [vectorstore.IDDeleter].
func (s *Store) DeleteIDs(ctx context.Context, ids []string) (err error) {
	if len(ids) == 0 {
		return nil
	}

	var body bytes.Buffer
	for _, id := range ids {
		var actionLine []byte
		actionLine, err = json.Marshal(bulkAction{
			Delete: &bulkActionTarget{Index: s.indexName, ID: id},
		})
		if err != nil {
			return fmt.Errorf("elasticsearch: encode bulk delete action: %w", err)
		}
		body.Write(actionLine)
		body.WriteByte(bulkRecordSeparator)
	}

	resp, err := s.client.Bulk(
		bytes.NewReader(body.Bytes()),
		s.client.Bulk.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("elasticsearch: bulk delete: %w", err)
	}
	return parseBulkResponse(resp, bulkOperationDelete)
}

// buildFilterQuery converts the AST filter into a Lucene query string
// for `query_string`. Returns "" when filter is nil.
func (s *Store) buildFilterQuery(expr filter.Predicate) (string, error) {
	if expr == nil {
		return "", nil
	}
	v := newVisitor(s.metadataField)
	if err := expr.Accept(v); err != nil {
		return "", fmt.Errorf("elasticsearch: convert filter: %w", err)
	}
	return v.snapshot(), nil
}

// normalizeScore validates Elasticsearch's already normalized dense-vector
// score. cosine and dot_product return (1+similarity)/2; l2_norm returns
// 1/(1+distance²). All three are in [0,1] with higher values ranked first.
func (s *Store) normalizeScore(score float64) vectorstore.Score {
	return vectorstore.ScoreFromValue(score)
}

func (s *Store) toDocument(hit searchHit) (*document.Document, error) {
	if hit.ID == "" {
		return nil, errors.New("elasticsearch: search hit is missing _id")
	}
	doc := &document.Document{ID: hit.ID}
	if hit.Source == nil {
		return nil, fmt.Errorf("elasticsearch: search hit %s is missing _source", hit.ID)
	}

	// Pull the document text from the configured content field.
	content, present, err := hit.Source.Decode[string](s.contentField)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: search hit %s field %q: %w", hit.ID, s.contentField, err)
	}
	if !present || content == "" {
		return nil, fmt.Errorf("elasticsearch: search hit %s is missing string field %q", hit.ID, s.contentField)
	}
	doc.Text = content

	doc.Metadata, err = s.hitMetadata(hit)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// hitMetadata decodes the stored metadata object without routing it through
// map[string]any, which would render every JSON number as a float64 and lose
// integers the caller stored exactly.
func (s *Store) hitMetadata(hit searchHit) (metadata.Map, error) {
	raw, present := hit.Source[s.metadataField]
	if !present || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values metadata.Map
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("elasticsearch: search hit %s field %q must be an object: %w",
			hit.ID, s.metadataField, err)
	}
	return values, nil
}

// checkSearchCompleteness rejects a result assembled from fewer shards than the
// query targeted. Returning those hits would present a partial index as the
// whole one, and the caller cannot tell the difference from a small result.
func (s *Store) checkSearchCompleteness(response searchResponse) error {
	if response.Shards.Failed > 0 {
		reason := "provider returned no reason"
		if len(response.Shards.Failures) > 0 {
			failure := response.Shards.Failures[0]
			if failure.Reason != nil && failure.Reason.Reason != "" {
				reason = failure.Reason.Reason
			}
		}
		return fmt.Errorf("elasticsearch: search %s failed on %d of %d shard(s): %s",
			s.indexName, response.Shards.Failed, response.Shards.Total, reason)
	}
	if response.TimedOut {
		return fmt.Errorf("elasticsearch: search %s timed out and returned partial hits", s.indexName)
	}
	return nil
}

func readErrorResponse(reader io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, maximumErrorResponseBytes))
}

// Index embeds the documents and bulk-indexes them.
