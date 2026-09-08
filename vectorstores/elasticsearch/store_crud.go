package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdmath "math"

	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// Elasticsearch bulk endpoints use newline-delimited JSON, including a final
// separator after the last record.
const bulkRecordSeparator = '\n'

type bulkOperation string

const (
	bulkOperationIndex  bulkOperation = "index"
	bulkOperationDelete bulkOperation = "delete"
)

type bulkAction struct {
	Index  *bulkActionTarget `json:"index,omitempty"`
	Delete *bulkActionTarget `json:"delete,omitempty"`
}

type bulkActionTarget struct {
	Index string `json:"_index"`
	ID    string `json:"_id"`
}

type queryString struct {
	Query string `json:"query"`
}

type queryClause struct {
	QueryString queryString `json:"query_string"`
}

type nearestNeighborQuery struct {
	Field         string       `json:"field"`
	QueryVector   []float32    `json:"query_vector"`
	K             int          `json:"k"`
	NumCandidates int          `json:"num_candidates"`
	Filter        *queryClause `json:"filter,omitempty"`
}

type searchRequest struct {
	Size int                  `json:"size"`
	KNN  nearestNeighborQuery `json:"knn"`
}

type deleteByQueryRequest struct {
	Query queryClause `json:"query"`
}

func (s *Store) Index(ctx context.Context, request *vectorstore.IndexRequest) (err error) {
	if validateErr := request.Validate(); validateErr != nil {
		return fmt.Errorf("elasticsearch.Store.Index: %w", validateErr)
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

func (s *Store) Close() error { return nil }

// These response models intentionally cover only fields consumed by Store;
// decoding remains forward-compatible without exposing Elasticsearch DTOs.
// TimedOut and Shards are consumed because Elasticsearch answers a partially
// executed search with 200 and reports the shortfall only in the body.
type searchResponse struct {
	TimedOut bool         `json:"timed_out"`
	Shards   searchShards `json:"_shards"`
	Hits     struct {
		Hits []searchHit `json:"hits"`
	} `json:"hits"`
}

// searchShards omits skipped shards on purpose: skipping is a normal
// pre-filtering outcome, while a failed shard means its documents were never
// searched.
type searchShards struct {
	Total    int             `json:"total"`
	Failed   int             `json:"failed"`
	Failures []searchFailure `json:"failures"`
}

type searchFailure struct {
	Index  string       `json:"index"`
	Shard  int          `json:"shard"`
	Reason *bulkFailure `json:"reason"`
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

// Source stays raw so stored numbers keep the exact spelling Elasticsearch
// returned; decoding through map[string]any would collapse every integer into
// a float64.
type searchHit struct {
	ID     string       `json:"_id"`
	Score  float64      `json:"_score"`
	Source metadata.Map `json:"_source"`
}

// deleteByQueryResponse carries the completeness facts Elasticsearch reports
// inside a 200 response. Total counts matched candidates and Deleted counts
// applied deletions, so the two diverge whenever documents were skipped.
type deleteByQueryResponse struct {
	TimedOut         bool                   `json:"timed_out"`
	Total            int64                  `json:"total"`
	Deleted          int64                  `json:"deleted"`
	VersionConflicts int64                  `json:"version_conflicts"`
	Failures         []deleteByQueryFailure `json:"failures"`
}

type deleteByQueryFailure struct {
	ID     string      `json:"id"`
	Status int         `json:"status"`
	Cause  bulkFailure `json:"cause"`
}

func (d deleteByQueryResponse) firstFailure() *deleteByQueryFailure {
	if len(d.Failures) == 0 {
		return nil
	}
	return &d.Failures[0]
}

type bulkResponse struct {
	Errors bool       `json:"errors"`
	Items  []bulkItem `json:"items"`
}

type bulkItem struct {
	Index  *bulkItemResult `json:"index"`
	Delete *bulkItemResult `json:"delete"`
}

func (b bulkItem) result(operation bulkOperation) *bulkItemResult {
	switch operation {
	case bulkOperationIndex:
		return b.Index
	case bulkOperationDelete:
		return b.Delete
	default:
		return nil
	}
}

type bulkItemResult struct {
	ID     string       `json:"_id"`
	Status int          `json:"status"`
	Error  *bulkFailure `json:"error"`
}

type bulkFailure struct {
	Reason string `json:"reason"`
}

func (b bulkResponse) firstFailure(operation bulkOperation) *bulkItemResult {
	for _, item := range b.Items {
		result := item.result(operation)
		if result != nil && result.Error != nil {
			return result
		}
	}
	return nil
}

func parseBulkResponse(response *esapi.Response, operation bulkOperation) (err error) {
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("elasticsearch: close bulk %s response: %w", operation, closeErr))
		}
	}()
	if response.IsError() {
		body, readErr := readErrorResponse(response.Body)
		if readErr != nil {
			return fmt.Errorf("elasticsearch: read bulk %s error response with status %d: %w",
				operation, response.StatusCode, readErr)
		}
		return fmt.Errorf("elasticsearch: bulk %s: status=%d body=%s",
			operation, response.StatusCode, string(body))
	}

	var parsed bulkResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("elasticsearch: decode bulk %s response: %w", operation, err)
	}
	if !parsed.Errors {
		return nil
	}
	failure := parsed.firstFailure(operation)
	if failure == nil {
		return fmt.Errorf("elasticsearch: bulk %s failed without an item error", operation)
	}
	reason := failure.Error.Reason
	if reason == "" {
		reason = "provider returned no reason"
	}
	return fmt.Errorf("elasticsearch: bulk %s failed for document %q with status %d: %s",
		operation, failure.ID, failure.Status, reason)
}

func encodeJSONRequest(value any) (io.Reader, error) {
	buf, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: encode request: %w", err)
	}
	return bytes.NewReader(buf), nil
}
