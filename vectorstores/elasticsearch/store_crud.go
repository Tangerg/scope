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
	defer resp.Body.Close()
	if resp.IsError() {
		respBody, readErr := readErrorResponse(resp.Body)
		if readErr != nil {
			return fmt.Errorf("elasticsearch: read delete_by_query error response for %s with status %d: %w",
				s.indexName, resp.StatusCode, readErr)
		}
		return fmt.Errorf("elasticsearch: delete_by_query %s: status=%d body=%s",
			s.indexName, resp.StatusCode, string(respBody))
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
	content, ok := hit.Source[s.contentField].(string)
	if !ok || content == "" {
		return nil, fmt.Errorf("elasticsearch: search hit %s is missing string field %q", hit.ID, s.contentField)
	}
	doc.Text = content

	metadataValues, err := s.metadataValues(hit)
	if err != nil {
		return nil, err
	}
	doc.Metadata, err = metadata.FromValues(metadataValues)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: convert metadata: %w", err)
	}
	return doc, nil
}

func (s *Store) metadataValues(hit searchHit) (map[string]any, error) {
	raw := hit.Source[s.metadataField]
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("elasticsearch: search hit %s field %q must be an object, got %T", hit.ID, s.metadataField, raw)
	}
	return values, nil
}

func (s *Store) Close() error { return nil }

// These response models intentionally cover only fields consumed by Store;
// decoding remains forward-compatible without exposing Elasticsearch DTOs.
type searchResponse struct {
	Hits struct {
		Hits []searchHit `json:"hits"`
	} `json:"hits"`
}

type searchHit struct {
	ID     string         `json:"_id"`
	Score  float64        `json:"_score"`
	Source map[string]any `json:"_source"`
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
