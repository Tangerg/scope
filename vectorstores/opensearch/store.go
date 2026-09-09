package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

const maximumErrorResponseBytes = int64(64 * 1024)

var (
	// ErrIndexMissing reports an absent index that this store was not asked to
	// create.
	ErrIndexMissing = errors.New("opensearch: index not found")

	// ErrIncompatibleIndex reports an existing index whose vector field cannot
	// serve this store: it is absent, not a knn_vector, or built for a
	// different space type or width.
	ErrIncompatibleIndex = errors.New("opensearch: index is incompatible")
)

var (
	_ vectorstore.Indexer       = (*Store)(nil)
	_ vectorstore.Searcher      = (*Store)(nil)
	_ vectorstore.FilterDeleter = (*Store)(nil)
	_ vectorstore.IDDeleter     = (*Store)(nil)
)

// Store implements vector-store capabilities with OpenSearch.
type Store struct {
	client          *opensearchapi.Client
	indexName       string
	embeddingField  string
	contentField    string
	metadataField   string
	embeddingClient embeddingclient.Client
	documentBatcher vectorstore.Batcher
	dimensions      int
	spaceType       SpaceType
	engine          Engine
	methodName      string
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
		return nil, fmt.Errorf("opensearch: create embedding client: %w", err)
	}

	store := &Store{
		client:          config.Client,
		indexName:       config.IndexName,
		embeddingField:  config.EmbeddingField,
		contentField:    config.ContentField,
		metadataField:   config.MetadataField,
		embeddingClient: embeddingClient,
		documentBatcher: config.DocumentBatcher,
		dimensions:      config.Dimensions,
		spaceType:       config.SpaceType,
		engine:          config.Engine,
		methodName:      config.MethodName,
	}

	if err = store.initialize(ctx, config.InitializeSchema); err != nil {
		return nil, fmt.Errorf("opensearch: initialize store: %w", err)
	}
	return store, nil
}

// initialize creates the index when needed, and confirms an existing one
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
		return errors.New("opensearch: embedding dimensions must be positive")
	}

	return s.createIndex(ctx)
}

func (s *Store) indexExists(ctx context.Context) (bool, error) {
	resp, err := s.client.Indices.Exists(ctx, opensearchapi.IndicesExistsReq{Indices: []string{s.indexName}})
	if err != nil {
		return false, fmt.Errorf("opensearch: check index %q: %w", s.indexName, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		body, readErr := readErrorResponse(resp.Body)
		if readErr != nil {
			return false, fmt.Errorf("opensearch: read index existence error for %q with status %d: %w",
				s.indexName, resp.StatusCode, readErr)
		}
		return false, fmt.Errorf("opensearch: check index %q: status=%d body=%s",
			s.indexName, resp.StatusCode, string(body))
	}
}

// verifyVectorField refuses an existing index whose vector field cannot serve
// this store.
//
// The space type is the one that fails quietly. OpenSearch derives _score from
// the field's own space -- "(2 - d) / 2" for cosinesimil is not "1 / (1 + d)"
// for l2 -- and innerproduct is the only space whose score runs above 1, which
// is why [SpaceType.score] inverts that encoding and passes the others
// through. A store configured for one space against a field built for another
// therefore either applies the inverse to a number it does not describe, or
// clamps unbounded inner-product scores onto Core's ceiling so an exact match
// and a mediocre one become the same value. Neither is visible downstream.
//
// The space type is optional in two places and defaulted in a third, so all
// three are read: the field carries it, "this value can also be specified
// within the method", and it "defaults to l2" when neither does.
func (s *Store) verifyVectorField(ctx context.Context) error {
	response, err := s.client.Indices.Mapping.Get(ctx, &opensearchapi.MappingGetReq{
		Indices: []string{s.indexName},
	})
	if err != nil {
		return fmt.Errorf("opensearch: read mapping for %q: %w", s.indexName, err)
	}
	if response == nil {
		return fmt.Errorf("opensearch: nil mapping response for %q", s.indexName)
	}

	// The response is keyed by resolved index name, which differs from the
	// configured one whenever it names an alias, so the single entry is read
	// rather than looked up.
	indices := response.GetIndices()
	if len(indices) != 1 {
		return fmt.Errorf("opensearch: mapping for %q resolved to %d indices; point the store at one",
			s.indexName, len(indices))
	}
	for _, index := range indices {
		var mappings struct {
			Properties map[string]storedVectorField `json:"properties"`
		}
		if err := json.Unmarshal(index.Mappings, &mappings); err != nil {
			return fmt.Errorf("opensearch: decode mapping for %q: %w", s.indexName, err)
		}
		return s.validateVectorField(mappings.Properties[s.embeddingField])
	}
	return nil
}

func (s *Store) validateVectorField(field storedVectorField) error {
	if field.Type == "" {
		return fmt.Errorf("%w: index %q declares no field named %q",
			ErrIncompatibleIndex, s.indexName, s.embeddingField)
	}
	if field.Type != mappingTypeVector {
		return fmt.Errorf("%w: field %q has type %q, want %q",
			ErrIncompatibleIndex, s.embeddingField, field.Type, mappingTypeVector)
	}
	spaceType, err := field.effectiveSpaceType()
	if err != nil {
		return fmt.Errorf("%w: field %q: %w", ErrIncompatibleIndex, s.embeddingField, err)
	}
	if spaceType != s.spaceType {
		return fmt.Errorf("%w: field %q is built for space type %q, but the store is configured for %q, and _score means something different under each",
			ErrIncompatibleIndex, s.embeddingField, spaceType, s.spaceType)
	}
	if s.dimensions > 0 && field.Dimensions > 0 && field.Dimensions != s.dimensions {
		return fmt.Errorf("%w: field %q holds %d dimensions, but the store is configured for %d",
			ErrIncompatibleIndex, s.embeddingField, field.Dimensions, s.dimensions)
	}
	return nil
}

func (s *Store) createIndex(ctx context.Context) error {
	embeddingMapping := vectorFieldMapping{
		Type:       mappingTypeVector,
		Dimensions: s.dimensions,
		Method: annMethodMapping{
			Name: s.methodName, Engine: s.engine, SpaceType: s.spaceType,
		},
	}
	properties := map[string]any{
		s.contentField:   textFieldMapping{Type: mappingTypeText},
		s.embeddingField: embeddingMapping,
		s.metadataField:  objectFieldMapping{Type: mappingTypeObject, Dynamic: true},
	}

	body, err := encodeJSONRequest(createIndexRequest{
		Settings: indexSettings{KNN: true},
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

	resp, err := s.client.Indices.Create(ctx, opensearchapi.IndicesCreateReq{
		Index: s.indexName,
		Body:  body,
	})
	if err != nil {
		return fmt.Errorf("opensearch: create index %q: %w", s.indexName, err)
	}
	if resp != nil && resp.Inspect().Response != nil && resp.Inspect().Response.IsError() {
		response := resp.Inspect().Response
		raw, readErr := readErrorResponse(response.Body)
		if readErr != nil {
			return fmt.Errorf("opensearch: read create-index error for %q with status %d: %w",
				s.indexName, response.StatusCode, readErr)
		}
		return fmt.Errorf("opensearch: create index %q: status=%d body=%s",
			s.indexName, response.StatusCode, string(raw))
	}
	return nil
}

func readErrorResponse(reader io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, maximumErrorResponseBytes))
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("opensearch.Store.Index: %w", validateErr)
	}

	var batches []*vectorstore.IndexRequest
	batches, err = request.Batch(ctx, s.documentBatcher)
	if err != nil {
		return fmt.Errorf("opensearch: batch documents: %w", err)
	}

	for _, batch := range batches {
		docs := batch.Documents
		texts, err := batch.Texts()
		if err != nil {
			return fmt.Errorf("vectorstore: project document text: %w", err)
		}
		vectors, err := s.embeddingClient.EmbedTexts(ctx, texts)
		if err != nil {
			return fmt.Errorf("opensearch: embed documents: %w", err)
		}

		var body bytes.Buffer
		for index, doc := range docs {
			id := doc.ID

			actionLine, encErr := json.Marshal(bulkAction{
				Index: &bulkActionTarget{ID: id},
			})
			if encErr != nil {
				return fmt.Errorf("opensearch: encode bulk action: %w", encErr)
			}

			docBody := map[string]any{
				s.contentField:   doc.Text,
				s.embeddingField: embedding.Float32Vector(vectors[index]),
				s.metadataField:  doc.Metadata,
			}
			docLine, encErr := json.Marshal(docBody)
			if encErr != nil {
				return fmt.Errorf("opensearch: encode bulk doc: %w", encErr)
			}

			body.Write(actionLine)
			body.WriteByte(bulkRecordSeparator)
			body.Write(docLine)
			body.WriteByte(bulkRecordSeparator)
		}

		resp, err := s.client.Bulk(ctx, opensearchapi.BulkReq{
			Index: s.indexName,
			Body:  bytes.NewReader(body.Bytes()),
		})
		if err != nil {
			return fmt.Errorf("opensearch: bulk: %w", err)
		}
		if err := (bulkOutcome{operation: bulkOperationIndex, response: resp}).Err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Search(ctx context.Context, req *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	var docs []*vectorstore.SearchResult
	if err = req.Validate(); err != nil {
		return nil, fmt.Errorf("opensearch.Store.Search: %w", err)
	}
	if err = req.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("opensearch.Store.Search: %w", err)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(req)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("opensearch: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)

	neighbor := nearestNeighbor{
		Vector: queryVec,
		K:      req.Options.ResultLimit(),
	}
	filterQuery, err := s.buildFilterQuery(req.Options.Filter)
	if err != nil {
		return nil, err
	}
	if filterQuery != "" {
		neighbor.Filter = &queryClause{QueryString: queryString{Query: filterQuery}}
	}

	body, err := encodeJSONRequest(searchRequest{
		Size: req.Options.ResultLimit(),
		Query: nearestNeighborQuery{
			KNN: map[string]nearestNeighbor{s.embeddingField: neighbor},
		},
	})
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Search(ctx, &opensearchapi.SearchReq{
		Indices: []string{s.indexName},
		Body:    body,
	})
	if err != nil {
		return nil, fmt.Errorf("opensearch: search %s: %w", s.indexName, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("opensearch: nil response for %s", s.indexName)
	}
	if err := s.checkSearchCompleteness(resp); err != nil {
		return nil, err
	}

	docs = make([]*vectorstore.SearchResult, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		score, scoreErr := s.spaceType.score(float64(hit.Score))
		if scoreErr != nil {
			return nil, scoreErr
		}
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

func (s *Store) DeleteWhere(ctx context.Context, expr filter.Predicate) (err error) {
	if expr == nil {
		return vectorstore.ErrMissingFilter
	}
	if err = expr.Validate(); err != nil {
		return fmt.Errorf("opensearch.Store.DeleteWhere: %w", err)
	}

	var filterQuery string
	filterQuery, err = s.buildFilterQuery(expr)
	if err != nil {
		return err
	}
	if filterQuery == "" {
		return errors.New("opensearch: refusing to delete on empty filter")
	}

	body, err := encodeJSONRequest(deleteByQueryRequest{
		Query: queryClause{QueryString: queryString{Query: filterQuery}},
	})
	if err != nil {
		return err
	}

	resp, err := s.client.Document.DeleteByQuery(ctx, opensearchapi.DocumentDeleteByQueryReq{
		Indices: []string{s.indexName},
		Body:    body,
	})
	if err != nil {
		return fmt.Errorf("opensearch: delete_by_query %s: %w", s.indexName, err)
	}
	if resp == nil {
		return fmt.Errorf("opensearch: nil delete_by_query response for %s", s.indexName)
	}
	return s.checkDeleteByQueryCompleteness(resp)
}

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
			return fmt.Errorf("opensearch: encode bulk delete action: %w", err)
		}
		body.Write(actionLine)
		body.WriteByte(bulkRecordSeparator)
	}

	resp, err := s.client.Bulk(ctx, opensearchapi.BulkReq{
		Index: s.indexName,
		Body:  bytes.NewReader(body.Bytes()),
	})
	if err != nil {
		return fmt.Errorf("opensearch: bulk delete: %w", err)
	}
	return (bulkOutcome{operation: bulkOperationDelete, response: resp}).Err()
}

// buildFilterQuery wraps the visitor and returns the Lucene query
// string suitable for the knn filter.
func (s *Store) buildFilterQuery(expr filter.Predicate) (string, error) {
	if expr == nil {
		return "", nil
	}
	v := newVisitor(s.metadataField)
	if err := expr.Accept(v); err != nil {
		return "", fmt.Errorf("opensearch: convert filter: %w", err)
	}
	return v.snapshot(), nil
}

func (s *Store) toDocument(hit opensearchapi.SearchHit) (*document.Document, error) {
	if hit.ID == "" {
		return nil, errors.New("opensearch: search hit is missing _id")
	}
	doc := &document.Document{ID: hit.ID}
	if len(hit.Source) == 0 {
		return nil, fmt.Errorf("opensearch: search hit %s is missing _source", hit.ID)
	}

	var source map[string]any
	if err := json.Unmarshal(hit.Source, &source); err != nil {
		return nil, fmt.Errorf("opensearch: decode _source for %s: %w", hit.ID, err)
	}

	content, ok := source[s.contentField].(string)
	if !ok || content == "" {
		return nil, fmt.Errorf("opensearch: search hit %s is missing string field %q", hit.ID, s.contentField)
	}
	doc.Text = content

	metadataValues, err := s.metadataValues(hit.ID, source)
	if err != nil {
		return nil, err
	}
	doc.Metadata, err = metadata.FromValues(metadataValues)
	if err != nil {
		return nil, fmt.Errorf("opensearch: convert metadata: %w", err)
	}
	return doc, nil
}

func (s *Store) metadataValues(id string, source map[string]any) (map[string]any, error) {
	raw := source[s.metadataField]
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("opensearch: search hit %s field %q must be an object, got %T", id, s.metadataField, raw)
	}
	return values, nil
}
