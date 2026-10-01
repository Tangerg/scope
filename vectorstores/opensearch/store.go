package opensearch

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

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
	if !exists {
		if !initSchema {
			return fmt.Errorf("%w: %q, and InitializeSchema is false", ErrIndexMissing, s.indexName)
		}
		if s.dimensions <= 0 {
			return errors.New("opensearch: embedding dimensions must be positive")
		}
		if err := s.createIndex(ctx); err != nil {
			return err
		}
	}
	if err := s.verifyVectorField(ctx); err != nil {
		return err
	}
	return s.verifySourceSettings(ctx)
}

func (s *Store) indexExists(ctx context.Context) (bool, error) {
	resp, err := s.client.Indices.Exists(ctx, opensearchapi.IndicesExistsReq{Indices: []string{s.indexName}})
	// The official SDK also returns an error for an empty HEAD 404 body.
	// The HTTP status is the existence result, not a failed creation attempt.
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("opensearch: check index %q: %w", s.indexName, err)
	}
	if resp == nil {
		return false, fmt.Errorf("opensearch: check index %q returned no response", s.indexName)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
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

	// An alias can add filtering and routing. Replacing it with the resolved
	// physical index would silently broaden the store's data boundary.
	indices := response.GetIndices()
	if len(indices) != 1 {
		return fmt.Errorf("opensearch: mapping for %q resolved to %d indices; point the store at one",
			s.indexName, len(indices))
	}
	for indexName, index := range indices {
		if indexName != s.indexName {
			return fmt.Errorf("%w: index %q resolved to %q; only concrete indices are supported", ErrIncompatibleIndex, s.indexName, indexName)
		}
		var mappings struct {
			Properties map[string]storedVectorField `json:"properties"`
			Source     storedSource                 `json:"_source"`
		}
		if err := jsonv2.Unmarshal(index.Mappings, &mappings); err != nil {
			return fmt.Errorf("opensearch: decode mapping for %q: %w", s.indexName, err)
		}
		if err := mappings.Source.validate(s.metadataField, s.contentField); err != nil {
			return err
		}
		if err := s.validateVectorField(mappings.Properties[s.embeddingField]); err != nil {
			return err
		}
		s.engine = mappings.Properties[s.embeddingField].Method.Engine
		return nil
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
	if field.Method == nil || !field.Method.Engine.Valid() {
		return fmt.Errorf("%w: field %q must declare a supported engine in its method; an omitted engine depends on the index's OpenSearch version", ErrIncompatibleIndex, s.embeddingField)
	}
	if field.Method.Engine != s.engine {
		return fmt.Errorf("%w: field %q uses engine %q, but the store is configured for %q", ErrIncompatibleIndex, s.embeddingField, field.Method.Engine, s.engine)
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
	for index, doc := range request.Documents {
		if doc.Media != nil {
			return fmt.Errorf("opensearch.Store.Index: %w: documents[%d] contains unsupported media", vectorstore.ErrInvalidDocument, index)
		}
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
		expectedIDs := make([]string, len(docs))
		for index, doc := range docs {
			expectedIDs[index] = doc.ID
			id := doc.ID

			actionLine, encErr := jsonv2.Marshal(bulkAction{
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
			docLine, encErr := jsonv2.Marshal(docBody)
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
		if err := (bulkOutcome{operation: bulkOperationIndex, response: resp, expectedIDs: expectedIDs}).err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Search(ctx context.Context, request *vectorstore.SearchRequest) (response *vectorstore.SearchResponse, err error) {
	if err = request.Validate(); err != nil {
		return nil, fmt.Errorf("opensearch.Store.Search: %w", err)
	}
	if err = request.Options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, fmt.Errorf("opensearch.Store.Search: %w", err)
	}
	if request.Options.Filter != nil && s.engine != EngineLucene && s.engine != EngineFaiss {
		return nil, fmt.Errorf("%w: opensearch: filtered KNN requires a Lucene or Faiss index, got engine %q", errors.ErrUnsupported, s.engine)
	}

	defer func() {
		if err == nil {
			err = response.ValidateFor(request)
		}
	}()

	vector, err := s.embeddingClient.EmbedText(ctx, request.Query)
	if err != nil {
		return nil, fmt.Errorf("opensearch: embed query: %w", err)
	}
	queryVec := embedding.Float32Vector(vector)
	var docs []scoredDocument
	if request.Options.Filter == nil {
		docs, err = s.searchVectors(ctx, request, queryVec, nil)
		if err != nil {
			return nil, err
		}
	} else {
		matches, selectErr := s.selectMatches(ctx, request.Options.Filter)
		if selectErr != nil {
			return nil, selectErr
		}
		for batch := range slices.Chunk(matches, filterBatchSize) {
			ids := make([]string, len(batch))
			for index, hit := range batch {
				ids[index] = hit.ID
			}
			partial, searchErr := s.searchVectors(ctx, request, queryVec, ids)
			if searchErr != nil {
				return nil, searchErr
			}
			docs = append(docs, partial...)
		}
	}
	slices.SortFunc(docs, func(left, right scoredDocument) int {
		if left.rank != right.rank {
			return cmp.Compare(right.rank, left.rank)
		}
		return cmp.Compare(left.document.ID, right.document.ID)
	})
	if len(docs) > request.Options.ResultLimit() {
		docs = docs[:request.Options.ResultLimit()]
	}
	results := make([]*vectorstore.SearchResult, len(docs))
	for index, doc := range docs {
		results[index] = &vectorstore.SearchResult{Document: doc.document, Score: doc.score}
	}
	return &vectorstore.SearchResponse{Results: results}, nil
}

// scoredDocument retains native rank until batches have been merged. Core
// scores may saturate during normalization and cannot reconstruct that order.
type scoredDocument struct {
	document *document.Document
	score    vectorstore.Score
	rank     float32
}

func (s *Store) searchVectors(ctx context.Context, req *vectorstore.SearchRequest, queryVec []float32, ids []string) ([]scoredDocument, error) {
	neighbor := nearestNeighbor{
		Vector: queryVec,
		K:      req.Options.ResultLimit(),
	}
	if ids != nil {
		neighbor.Filter = &queryClause{IDs: idsQuery{Values: ids}}
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

	allowedIDs := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowedIDs[id] = struct{}{}
	}
	docs := make([]scoredDocument, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		if ids != nil {
			if _, allowed := allowedIDs[hit.ID]; !allowed {
				return nil, fmt.Errorf("%w: opensearch: filtered search returned document %q outside the selected ID batch", vectorstore.ErrInvalidResponse, hit.ID)
			}
		}
		doc, decodeErr := s.toDocument(hit)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if req.Options.Filter != nil {
			values, valuesErr := doc.Metadata.Values()
			if valuesErr != nil {
				return nil, fmt.Errorf("opensearch: decode returned metadata for %q: %w", hit.ID, valuesErr)
			}
			matched, matchErr := filter.Match(req.Options.Filter, values)
			if matchErr != nil {
				return nil, fmt.Errorf("opensearch: evaluate returned metadata for %q: %w", hit.ID, matchErr)
			}
			if !matched {
				return nil, fmt.Errorf("%w: opensearch: returned document %q no longer matches the filter", vectorstore.ErrInvalidResponse, hit.ID)
			}
		}
		score, scoreErr := s.spaceType.score(float64(hit.Score))
		if scoreErr != nil {
			return nil, scoreErr
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
func (s *Store) DeleteWhere(ctx context.Context, predicate filter.Predicate) error {
	if predicate == nil {
		return vectorstore.ErrMissingFilter
	}
	if err := predicate.Validate(); err != nil {
		return fmt.Errorf("opensearch.Store.DeleteWhere: %w", err)
	}
	matches, err := s.selectMatches(ctx, predicate)
	if err != nil {
		return err
	}
	for batch := range slices.Chunk(matches, filterBatchSize) {
		var body bytes.Buffer
		expectedIDs := make([]string, len(batch))
		for index, hit := range batch {
			expectedIDs[index] = hit.ID
			if hit.SeqNo == nil || hit.PrimaryTerm == nil {
				return fmt.Errorf("opensearch: matched document %q has no concurrency token", hit.ID)
			}
			action, err := jsonv2.Marshal(bulkAction{Delete: &bulkActionTarget{Index: s.indexName, ID: hit.ID, Routing: hit.Routing, SeqNo: hit.SeqNo, PrimaryTerm: hit.PrimaryTerm}})
			if err != nil {
				return fmt.Errorf("opensearch: encode conditional deletion: %w", err)
			}
			body.Write(action)
			body.WriteByte(bulkRecordSeparator)
		}
		response, err := s.client.Bulk(ctx, opensearchapi.BulkReq{Index: s.indexName, Body: &body})
		if err != nil {
			return fmt.Errorf("opensearch: conditional bulk deletion: %w", err)
		}
		if err := (bulkOutcome{operation: bulkOperationDelete, response: response, expectedIDs: expectedIDs}).err(); err != nil {
			return err
		}
	}
	return nil
}

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
	return (bulkOutcome{operation: bulkOperationDelete, response: resp, expectedIDs: ids}).err()
}

func (s *Store) toDocument(hit opensearchapi.SearchHit) (*document.Document, error) {
	if hit.ID == "" {
		return nil, errors.New("opensearch: search hit is missing _id")
	}
	doc := &document.Document{ID: hit.ID}
	if len(hit.Source) == 0 {
		return nil, fmt.Errorf("opensearch: search hit %s is missing _source", hit.ID)
	}

	var source map[string]json.RawMessage
	if err := jsonv2.Unmarshal(hit.Source, &source); err != nil {
		return nil, fmt.Errorf("opensearch: decode _source for %s: %w", hit.ID, err)
	}
	if err := jsonv2.Unmarshal(source[s.contentField], &doc.Text); err != nil || doc.Text == "" {
		return nil, fmt.Errorf("opensearch: search hit %s is missing string field %q", hit.ID, s.contentField)
	}
	if raw, present := source[s.metadataField]; present {
		if err := jsonv2.Unmarshal(raw, &doc.Metadata); err != nil {
			return nil, fmt.Errorf("opensearch: decode metadata field %q for %s: %w", s.metadataField, hit.ID, err)
		}
	}
	return doc, nil
}

// checkSearchCompleteness rejects a result assembled from fewer shards than the
// query targeted. OpenSearch answers a search that lost shards or ran out of
// time with 200 and the surviving hits, so a caller reading only the status
// cannot tell a partial index from a small result. Skipped shards are a normal
// pre-filtering outcome and stay acceptable.
func (s *Store) checkSearchCompleteness(response *opensearchapi.SearchResp) error {
	if response.Shards.Failed > 0 {
		reason := "provider returned no reason"
		if len(response.Shards.Failures) > 0 {
			if stated := response.Shards.Failures[0].Reason.Reason; stated != "" {
				reason = stated
			}
		}
		return fmt.Errorf("opensearch: search %s failed on %d of %d shard(s): %s",
			s.indexName, response.Shards.Failed, response.Shards.Total, reason)
	}
	if response.Timeout {
		return fmt.Errorf("opensearch: search %s timed out and returned partial hits", s.indexName)
	}
	return nil
}

// selectMatches evaluates raw metadata from one complete scroll snapshot;
// indexed terms cannot distinguish scalar values, arrays, and empty values.
func (s *Store) selectMatches(ctx context.Context, predicate filter.Predicate) (matches []opensearchapi.SearchHit, err error) {
	body, err := encodeJSONRequest(metadataScanRequest{Size: filterBatchSize, Source: []string{s.metadataField}, StoredFields: []string{"_routing"}, Sort: []string{"_doc"}, SeqNoPrimaryTerm: true})
	if err != nil {
		return nil, err
	}
	page, err := s.client.Search(ctx, &opensearchapi.SearchReq{Indices: []string{s.indexName}, Body: body, Params: opensearchapi.SearchParams{Scroll: filterScrollLifetime}})
	if err != nil {
		return nil, fmt.Errorf("opensearch: scan metadata: %w", err)
	}
	scrollID := ""
	defer func() {
		if scrollID == "" {
			return
		}
		// Cleanup owns a bounded context after the caller cancels the scan.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), filterCleanupTimeout)
		defer cancel()
		response, cleanupErr := s.client.Scroll.Delete(cleanup, opensearchapi.ScrollDeleteReq{ScrollIDs: []string{scrollID}})
		if cleanupErr == nil && (response == nil || !response.Succeeded) {
			cleanupErr = errors.New("opensearch: clear metadata scroll was not acknowledged")
		}
		err = errors.Join(err, cleanupErr)
	}()
	seen := make(map[string]struct{})
	for {
		if page == nil {
			return nil, errors.New("opensearch: metadata scan returned no page")
		}
		if page.ScrollID != nil {
			scrollID = *page.ScrollID
		}
		if err := s.checkSearchCompleteness(page); err != nil {
			return nil, err
		}
		if len(page.Hits.Hits) == 0 {
			return matches, nil
		}
		if page.ScrollID == nil || *page.ScrollID == "" {
			return nil, errors.New("opensearch: metadata scan omitted scroll ID")
		}
		for _, hit := range page.Hits.Hits {
			if err := context.Cause(ctx); err != nil {
				return nil, err
			}
			if hit.ID == "" || len(hit.Source) == 0 {
				return nil, errors.New("opensearch: metadata scan omitted document ID or source")
			}
			if _, duplicate := seen[hit.ID]; duplicate {
				return nil, fmt.Errorf("opensearch: metadata scan repeated document %q", hit.ID)
			}
			seen[hit.ID] = struct{}{}
			var source metadata.Map
			if err := jsonv2.Unmarshal(hit.Source, &source); err != nil {
				return nil, fmt.Errorf("opensearch: decode metadata source: %w", err)
			}
			values, _, err := source.Decode[metadata.Map](s.metadataField)
			if err != nil {
				return nil, fmt.Errorf("opensearch: decode stored metadata: %w", err)
			}
			data, err := values.Values()
			if err != nil {
				return nil, fmt.Errorf("opensearch: decode metadata values: %w", err)
			}
			matched, err := filter.Match(predicate, data)
			if err != nil {
				return nil, fmt.Errorf("opensearch: evaluate metadata for %q: %w", hit.ID, err)
			}
			if matched {
				hit.Source = nil
				matches = append(matches, hit)
			}
		}
		next, err := s.client.Scroll.Get(ctx, opensearchapi.ScrollGetReq{ScrollID: scrollID, Params: opensearchapi.ScrollGetParams{Scroll: filterScrollLifetime}})
		if err != nil {
			return nil, fmt.Errorf("opensearch: advance metadata scroll: %w", err)
		}
		if next == nil {
			return nil, errors.New("opensearch: metadata scroll returned no page")
		}
		if next.TerminatedEarly {
			return nil, errors.New("opensearch: metadata scroll terminated early")
		}
		page = &opensearchapi.SearchResp{Timeout: next.Timeout, Shards: next.Shards, ScrollID: next.ScrollID, Hits: opensearchapi.SearchHits{Hits: next.Hits.Hits}}
	}
}

const (
	filterBatchSize      = 512
	filterScrollLifetime = time.Minute
	filterCleanupTimeout = 5 * time.Second
)

func (s *Store) verifySourceSettings(ctx context.Context) error {
	response, err := s.client.Indices.Settings.Get(ctx, &opensearchapi.SettingsGetReq{Indices: []string{s.indexName}, Params: opensearchapi.SettingsGetParams{FlatSettings: new(true)}})
	if err != nil {
		return fmt.Errorf("opensearch: read source settings: %w", err)
	}
	if response == nil {
		return errors.New("opensearch: source settings returned no response")
	}
	indices := response.GetIndices()
	if len(indices) != 1 {
		return fmt.Errorf("opensearch: source settings resolved to %d indices", len(indices))
	}
	for indexName, index := range indices {
		if indexName != s.indexName {
			return fmt.Errorf("%w: source settings for %q resolved to %q", ErrIncompatibleIndex, s.indexName, indexName)
		}
		var settings struct {
			DerivedSourceEnabled string `json:"index.derived_source.enabled"`
		}
		if err := jsonv2.Unmarshal(index.Settings, &settings); err != nil {
			return fmt.Errorf("opensearch: decode source settings: %w", err)
		}
		if settings.DerivedSourceEnabled == "true" {
			return fmt.Errorf("%w: derived source cannot preserve original metadata", ErrIncompatibleIndex)
		}
	}
	return nil
}
