package elasticsearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/Tangerg/scope/core/metadata"
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
