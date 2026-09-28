package elasticsearch

import (
	"bytes"
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	stdmath "math"
	"net/http"
	"slices"
	"time"

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

// dynamicTemplate preserves the index creation policy for native metadata terms.
// Core predicates are evaluated from stored source, independently of these terms.
type dynamicTemplate struct {
	PathMatch        string         `json:"path_match"`
	MatchMappingType string         `json:"match_mapping_type"`
	Mapping          map[string]any `json:"mapping"`
}

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

	// IndexName names a concrete Elasticsearch index. Aliases are unsupported
	// because their filtering and routing cannot be discarded during selection.
	// Optional: defaults to [DefaultIndexName].
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
	if !exists {
		if !initSchema {
			return fmt.Errorf("%w: %q, and InitializeSchema is false", ErrIndexMissing, s.indexName)
		}
		if s.dimensions <= 0 {
			return errors.New("elasticsearch: Dimensions must be > 0")
		}
		if err := s.createIndex(ctx); err != nil {
			return err
		}
	}
	// Index templates also affect freshly created indices. Validate the actual
	// mapping and source policy after either construction path.
	if err := s.verifyVectorField(ctx); err != nil {
		return err
	}
	return s.verifySourceSettings(ctx)
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
// The deferred close preserves response cleanup failures.
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

	// Aliases can add filtering and routing. Rebinding to a physical index would
	// silently remove those constraints, while retargeting between selection and
	// deletion could address a different document. Only concrete indices bind.
	var mappings map[string]struct {
		Mappings struct {
			Properties map[string]storedVectorField `json:"properties"`
			Source     storedSource                 `json:"_source"`
		} `json:"mappings"`
	}
	if err := jsonv2.UnmarshalRead(response.Body, &mappings); err != nil {
		return fmt.Errorf("elasticsearch: decode mapping for %q: %w", s.indexName, err)
	}
	if len(mappings) != 1 {
		return fmt.Errorf("elasticsearch: mapping for %q resolved to %d indices; point the store at one",
			s.indexName, len(mappings))
	}
	for indexName, mapping := range mappings {
		if err := mapping.Mappings.Source.validate(s.metadataField, s.contentField); err != nil {
			return err
		}
		if err := s.validateVectorField(mapping.Mappings.Properties[s.embeddingField]); err != nil {
			return err
		}
		if indexName != s.indexName {
			return fmt.Errorf("%w: index %q resolves to %q; aliases cannot preserve filtering and routing across the metadata snapshot", errors.ErrUnsupported, s.indexName, indexName)
		}
		return nil
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
		expectedIDs := make([]string, len(docs))
		for index, doc := range docs {
			expectedIDs[index] = doc.ID
			id := doc.ID

			actionLine, encErr := jsonv2.Marshal(bulkAction{
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
			docLine, encErr := jsonv2.Marshal(docBody)
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
		if err = parseBulkResponse(resp, bulkOperationIndex, expectedIDs); err != nil {
			return err
		}
	}
	return nil
}

type scoredDocument struct {
	document *document.Document
	score    vectorstore.Score
	rank     float64
}

// Search runs a KNN search over the embedding field. Optional metadata filtering
// evaluates a complete metadata snapshot before KNN selection. A returned record
// that no longer satisfies the predicate causes the whole query to fail.
func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
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
	batches := [][]string{nil}
	if req.Options.Filter != nil {
		matches, err := s.selectMatches(ctx, req.Options.Filter)
		if err != nil {
			return nil, err
		}
		batches = nil
		for batch := range slices.Chunk(matches, filterBatchSize) {
			ids := make([]string, len(batch))
			for index, hit := range batch {
				ids[index] = hit.ID
			}
			batches = append(batches, ids)
		}
	}
	var docs []scoredDocument
	for _, ids := range batches {
		partial, err := s.searchVectors(ctx, req, queryVec, ids)
		if err != nil {
			return nil, err
		}
		docs = append(docs, partial...)
	}
	slices.SortFunc(docs, func(left, right scoredDocument) int {
		if left.rank != right.rank {
			return cmp.Compare(right.rank, left.rank)
		}
		return cmp.Compare(left.document.ID, right.document.ID)
	})
	if len(docs) > req.Options.ResultLimit() {
		docs = docs[:req.Options.ResultLimit()]
	}
	results := make([]*vectorstore.SearchResult, len(docs))
	for index, doc := range docs {
		results[index] = &vectorstore.SearchResult{Document: doc.document, Score: doc.score}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

func (s *Store) searchVectors(ctx context.Context, req *vectorstore.SearchRequest, queryVec []float32, ids []string) ([]scoredDocument, error) {

	knn := nearestNeighborQuery{
		Field:         s.embeddingField,
		QueryVector:   queryVec,
		K:             req.Options.ResultLimit(),
		NumCandidates: int(stdmath.Ceil(float64(req.Options.ResultLimit()) * s.numCandidatesMul)),
	}

	if ids != nil {
		knn.Filter = &queryClause{IDs: idsQuery{Values: ids}}
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
	if err = jsonv2.UnmarshalRead(resp.Body, &parsed); err != nil {
		return nil, fmt.Errorf("elasticsearch: decode search response: %w", err)
	}
	if err := s.checkSearchCompleteness(parsed); err != nil {
		return nil, err
	}

	docs := make([]scoredDocument, 0, len(parsed.Hits.Hits))
	for _, hit := range parsed.Hits.Hits {
		score := s.normalizeScore(hit.Score)
		doc, err := s.toDocument(hit)
		if err != nil {
			return nil, err
		}
		if ids != nil {
			if !slices.Contains(ids, hit.ID) {
				return nil, fmt.Errorf("elasticsearch: search returned unselected document %q", hit.ID)
			}
			values, err := doc.Metadata.Values()
			if err != nil {
				return nil, fmt.Errorf("elasticsearch: decode returned metadata: %w", err)
			}
			matched, err := filter.Match(req.Options.Filter, values)
			if err != nil {
				return nil, fmt.Errorf("elasticsearch: revalidate returned metadata: %w", err)
			}
			if !matched {
				return nil, fmt.Errorf("elasticsearch: document %q no longer satisfies the filter", hit.ID)
			}
		}
		if score < req.Options.MinScore {
			continue
		}
		docs = append(docs, scoredDocument{document: doc, score: score, rank: hit.Score})
	}
	return docs, nil
}

// DeleteWhere evaluates the complete stored metadata snapshot with Core's
// predicate semantics. Conditional bulk deletion refuses documents changed
// since that snapshot. A conflict returns an error; earlier batches stay deleted.
func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) error {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := expr.Validate(); err != nil {
		return fmt.Errorf("elasticsearch.Store.DeleteWhere: %w", err)
	}
	matches, err := s.selectMatches(ctx, expr)
	if err != nil {
		return err
	}
	for batch := range slices.Chunk(matches, filterBatchSize) {
		var body bytes.Buffer
		expectedIDs := make([]string, len(batch))
		for index, hit := range batch {
			expectedIDs[index] = hit.ID
			if hit.SeqNo == nil || hit.PrimaryTerm == nil {
				return fmt.Errorf("elasticsearch: matched document %q has no concurrency token", hit.ID)
			}
			action, err := jsonv2.Marshal(bulkAction{Delete: &bulkActionTarget{Index: s.indexName, ID: hit.ID, SeqNo: hit.SeqNo, PrimaryTerm: hit.PrimaryTerm, Routing: hit.Routing}})
			if err != nil {
				return fmt.Errorf("elasticsearch: encode conditional deletion: %w", err)
			}
			body.Write(action)
			body.WriteByte(bulkRecordSeparator)
		}
		response, err := s.client.Bulk(&body, s.client.Bulk.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("elasticsearch: conditional bulk deletion: %w", err)
		}
		if err := parseBulkResponse(response, bulkOperationDelete, expectedIDs); err != nil {
			return err
		}
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
		actionLine, err = jsonv2.Marshal(bulkAction{
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
	return parseBulkResponse(resp, bulkOperationDelete, ids)
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
	if err := jsonv2.Unmarshal(raw, &values); err != nil {
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

// selectMatches scans one server snapshot. Lucene's multivalued fields cannot
// preserve Core scalar/array/null distinctions, so no metadata expression is
// translated into Lucene syntax. Memory is proportional to the scanned document IDs.
func (s *Store) selectMatches(ctx context.Context, predicate filter.Predicate) (matches []searchHit, err error) {
	body, err := encodeJSONRequest(metadataScanRequest{Size: filterBatchSize, Source: []string{s.metadataField}, Sort: []string{"_doc"}, SeqNoPrimaryTerm: true, StoredFields: []string{"_routing"}})
	if err != nil {
		return nil, err
	}
	response, err := s.client.Search(s.client.Search.WithContext(ctx), s.client.Search.WithIndex(s.indexName), s.client.Search.WithBody(body), s.client.Search.WithScroll(filterScrollLifetime))
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: scan metadata: %w", err)
	}
	scrollID := ""
	defer func() {
		if scrollID == "" {
			return
		}
		// Cleanup owns a bounded context after the caller cancels the scan.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), filterCleanupTimeout)
		defer cancel()
		cleared, cleanupErr := s.client.ClearScroll(s.client.ClearScroll.WithContext(cleanup), s.client.ClearScroll.WithScrollID(scrollID))
		if cleanupErr == nil {
			if cleared.IsError() {
				cleanupErr = fmt.Errorf("elasticsearch: clear metadata scroll: HTTP %d", cleared.StatusCode)
			} else {
				var outcome struct {
					Succeeded bool `json:"succeeded"`
				}
				cleanupErr = jsonv2.UnmarshalRead(cleared.Body, &outcome)
				if cleanupErr == nil && !outcome.Succeeded {
					cleanupErr = errors.New("elasticsearch: metadata scroll cleanup was not acknowledged")
				}
			}
			cleanupErr = errors.Join(cleanupErr, cleared.Body.Close())
		}
		err = errors.Join(err, cleanupErr)
	}()
	seen := make(map[string]struct{})
	for {
		page, pageErr := s.readMetadataPage(response)
		if page.ScrollID != "" {
			scrollID = page.ScrollID
		}
		if pageErr != nil {
			return nil, pageErr
		}
		if completenessErr := s.checkSearchCompleteness(page); completenessErr != nil {
			return nil, completenessErr
		}
		if len(page.Hits.Hits) == 0 {
			return matches, nil
		}
		if page.ScrollID == "" {
			return nil, errors.New("elasticsearch: metadata scan omitted scroll ID")
		}
		for _, hit := range page.Hits.Hits {
			if contextErr := context.Cause(ctx); contextErr != nil {
				return nil, contextErr
			}
			if hit.ID == "" || hit.Source == nil {
				return nil, errors.New("elasticsearch: metadata scan omitted document ID or source")
			}
			if _, duplicate := seen[hit.ID]; duplicate {
				return nil, fmt.Errorf("elasticsearch: metadata scan repeated document %q", hit.ID)
			}
			seen[hit.ID] = struct{}{}
			values, metadataErr := s.hitMetadata(hit)
			if metadataErr != nil {
				return nil, metadataErr
			}
			data, valuesErr := values.Values()
			if valuesErr != nil {
				return nil, fmt.Errorf("elasticsearch: decode metadata values: %w", valuesErr)
			}
			matched, matchErr := filter.Match(predicate, data)
			if matchErr != nil {
				return nil, fmt.Errorf("elasticsearch: evaluate metadata for %q: %w", hit.ID, matchErr)
			}
			if matched {
				hit.Source = nil
				matches = append(matches, hit)
			}
		}
		response, err = s.client.Scroll(s.client.Scroll.WithContext(ctx), s.client.Scroll.WithScrollID(scrollID), s.client.Scroll.WithScroll(filterScrollLifetime))
		if err != nil {
			return nil, fmt.Errorf("elasticsearch: advance metadata scroll: %w", err)
		}
	}
}

func (s *Store) readMetadataPage(response *esapi.Response) (page searchResponse, err error) {
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	if response.IsError() {
		return page, fmt.Errorf("elasticsearch: read metadata page: HTTP %d", response.StatusCode)
	}
	if err := jsonv2.UnmarshalRead(response.Body, &page); err != nil {
		return page, fmt.Errorf("elasticsearch: decode metadata page: %w", err)
	}
	return page, nil
}

const (
	filterBatchSize      = 512
	filterScrollLifetime = time.Minute
	filterCleanupTimeout = 5 * time.Second
)

func (s *Store) verifySourceSettings(ctx context.Context) (err error) {
	response, err := s.client.Indices.GetSettings(s.client.Indices.GetSettings.WithContext(ctx), s.client.Indices.GetSettings.WithIndex(s.indexName), s.client.Indices.GetSettings.WithName("index.mapping.source.mode"), s.client.Indices.GetSettings.WithFlatSettings(true), s.client.Indices.GetSettings.WithIncludeDefaults(true))
	if err != nil {
		return fmt.Errorf("elasticsearch: read source settings: %w", err)
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	if response.IsError() {
		return fmt.Errorf("elasticsearch: read source settings: HTTP %d", response.StatusCode)
	}
	var indices map[string]struct {
		Settings map[string]string `json:"settings"`
		Defaults map[string]string `json:"defaults"`
	}
	if err := jsonv2.UnmarshalRead(response.Body, &indices); err != nil {
		return fmt.Errorf("elasticsearch: decode source settings: %w", err)
	}
	if len(indices) != 1 {
		return fmt.Errorf("elasticsearch: source settings resolved to %d indices", len(indices))
	}
	for _, index := range indices {
		mode := cmp.Or(index.Settings["index.mapping.source.mode"], index.Defaults["index.mapping.source.mode"], "stored")
		if mode != "stored" {
			return fmt.Errorf("%w: source mode %q cannot preserve original metadata", ErrIncompatibleIndex, mode)
		}
	}
	return nil
}
