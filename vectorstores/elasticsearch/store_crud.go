package elasticsearch

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

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
	Index  *bulkActionTarget `json:"index,omitzero"`
	Delete *bulkActionTarget `json:"delete,omitzero"`
}

type bulkActionTarget struct {
	Index       string `json:"_index"`
	ID          string `json:"_id"`
	SeqNo       *int   `json:"if_seq_no,omitzero"`
	PrimaryTerm *int   `json:"if_primary_term,omitzero"`
	Routing     string `json:"routing,omitzero"`
}

type idsQuery struct {
	Values []string `json:"values"`
}
type queryClause struct {
	IDs idsQuery `json:"ids"`
}

type metadataScanRequest struct {
	Size             int      `json:"size"`
	Source           []string `json:"_source"`
	Sort             []string `json:"sort"`
	SeqNoPrimaryTerm bool     `json:"seq_no_primary_term"`
	StoredFields     []string `json:"stored_fields"`
}

type nearestNeighborQuery struct {
	Field         string       `json:"field"`
	QueryVector   []float32    `json:"query_vector"`
	K             int          `json:"k"`
	NumCandidates int          `json:"num_candidates"`
	Filter        *queryClause `json:"filter,omitzero"`
}

type searchRequest struct {
	Size int                  `json:"size"`
	KNN  nearestNeighborQuery `json:"knn"`
}

// These response models intentionally cover only fields consumed by Store;
// decoding remains forward-compatible without exposing Elasticsearch DTOs.
// TimedOut and Shards are consumed because Elasticsearch answers a partially
// executed search with 200 and reports the shortfall only in the body.
type searchResponse struct {
	ScrollID string       `json:"_scroll_id"`
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
	ID          string       `json:"_id"`
	Routing     string       `json:"_routing"`
	SeqNo       *int         `json:"_seq_no"`
	PrimaryTerm *int         `json:"_primary_term"`
	Score       float64      `json:"_score"`
	Source      metadata.Map `json:"_source"`
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

func parseBulkResponse(response *esapi.Response, operation bulkOperation, expectedIDs []string) (err error) {
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
	if err := jsonv2.UnmarshalRead(response.Body, &parsed); err != nil {
		return fmt.Errorf("elasticsearch: decode bulk %s response: %w", operation, err)
	}
	if len(parsed.Items) != len(expectedIDs) {
		return fmt.Errorf("elasticsearch: bulk %s acknowledged %d of %d documents", operation, len(parsed.Items), len(expectedIDs))
	}
	for index, item := range parsed.Items {
		result := item.result(operation)
		if result == nil || result.ID != expectedIDs[index] {
			return fmt.Errorf("elasticsearch: bulk %s response item %d does not identify requested document %q", operation, index, expectedIDs[index])
		}
		success := result.Status >= 200 && result.Status < 300
		missing := operation == bulkOperationDelete && result.Status == 404
		if !success && !missing || result.Error != nil && !missing {
			reason := "provider returned no reason"
			if result.Error != nil && result.Error.Reason != "" {
				reason = result.Error.Reason
			}
			return fmt.Errorf("elasticsearch: bulk %s failed for document %q with status %d: %s", operation, result.ID, result.Status, reason)
		}
	}
	if parsed.Errors {
		return fmt.Errorf("elasticsearch: bulk %s reported an unexplained failure", operation)
	}
	return nil
}

func encodeJSONRequest(value any) (io.Reader, error) {
	buf, err := jsonv2.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("elasticsearch: encode request: %w", err)
	}
	return bytes.NewReader(buf), nil
}

type storedSource struct {
	Enabled  *bool    `json:"enabled"`
	Includes []string `json:"includes"`
	Excludes []string `json:"excludes"`
	Mode     string   `json:"mode"`
}

func (s storedSource) validate(fields ...string) error {
	if s.Enabled != nil && !*s.Enabled || s.Mode != "" && s.Mode != "stored" {
		return fmt.Errorf("%w: filtering requires stored _source", ErrIncompatibleIndex)
	}
	for _, field := range fields {
		if field == "" {
			continue
		}
		included := len(s.Includes) == 0
		for _, pattern := range s.Includes {
			if pattern == "*" || pattern == field {
				included = true
			}
		}
		if !included {
			return fmt.Errorf("%w: source includes must preserve complete field %q", ErrIncompatibleIndex, field)
		}
		for _, pattern := range s.Excludes {
			// A wildcard that can address this root may prune a descendant. Exact
			// exclusions of unrelated fields, such as embedding, remain acceptable.
			root := strings.SplitN(pattern, ".", 2)[0]
			match, err := path.Match(root, strings.SplitN(field, ".", 2)[0])
			if err != nil || match {
				return fmt.Errorf("%w: source exclusion %q may prune field %q", ErrIncompatibleIndex, pattern, field)
			}
		}
	}
	return nil
}
