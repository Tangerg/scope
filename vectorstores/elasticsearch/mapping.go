package elasticsearch

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type mappedIndex struct {
	Mappings nativeMapping `json:"mappings"`
}

type nativeMapping struct {
	Dynamic    string                 `json:"dynamic"`
	Properties map[string]mappedField `json:"properties"`
	Source     mappedSource           `json:"_source"`
	Routing    struct {
		Required bool `json:"required"`
	} `json:"_routing"`
	Runtime          map[string]any `json:"runtime"`
	DynamicTemplates []any          `json:"dynamic_templates"`
}

func (n nativeMapping) schema(settings nativeSettings) (nativeSchema, error) {
	var schema nativeSchema
	if n.Dynamic != "strict" || len(n.Properties) != 3 || n.Routing.Required || len(n.Runtime) > 0 || len(n.DynamicTemplates) > 0 {
		return schema, fmt.Errorf("%w: requires exactly the current strict properties without routing or dynamic rules", ErrIncompatibleIndex)
	}
	if n.Source.Enabled != nil && !*n.Source.Enabled || len(n.Source.Includes) > 0 || len(n.Source.Excludes) > 0 || n.Source.Mode != "" && n.Source.Mode != "stored" {
		return schema, fmt.Errorf("%w: requires complete stored _source", ErrIncompatibleIndex)
	}
	if field := n.Properties[contentField]; field.Type != "text" {
		return schema, fmt.Errorf("%w: content must be TEXT", ErrIncompatibleIndex)
	}
	if field := n.Properties[metadataField]; field.Type != "keyword" || field.Index == nil || *field.Index || field.DocValues == nil || *field.DocValues {
		return schema, fmt.Errorf("%w: metadata_json must be an unindexed KEYWORD without doc values", ErrIncompatibleIndex)
	}
	vector := n.Properties[embeddingField]
	if vector.Type != "dense_vector" || vector.Dimensions <= 0 || vector.Dimensions > 4096 || vector.Index != nil && !*vector.Index || vector.ElementType != "" && vector.ElementType != "float" {
		return schema, fmt.Errorf("%w: requires indexed FLOAT32 dense_vector with a declared dimension", ErrIncompatibleIndex)
	}
	schema.dimensions = vector.Dimensions
	schema.similarity = cmp.Or(vector.Similarity, "cosine")
	switch schema.similarity {
	case "cosine", "l2_norm", "dot_product", "max_inner_product":
	default:
		return schema, fmt.Errorf("%w: unsupported native similarity %q", ErrIncompatibleIndex, schema.similarity)
	}
	if !strings.EqualFold(settings.value("index.mapping.source.mode"), "stored") {
		return schema, fmt.Errorf("%w: source mode must be stored", ErrIncompatibleIndex)
	}
	for _, name := range []string{"index.default_pipeline", "index.final_pipeline"} {
		if value := settings.value(name); value != "_none" {
			return schema, fmt.Errorf("%w: native ingest pipeline %q can change document facts", ErrIncompatibleIndex, value)
		}
	}
	limit, err := strconv.Atoi(settings.value("index.max_result_window"))
	if err != nil || limit <= 0 {
		return schema, fmt.Errorf("%w: invalid native result window", ErrIncompatibleIndex)
	}
	schema.resultWindow = min(limit, nativeMaximumK)
	return schema, nil
}

type mappedField struct {
	Type        string `json:"type"`
	Dimensions  int    `json:"dims"`
	Similarity  string `json:"similarity"`
	ElementType string `json:"element_type"`
	Index       *bool  `json:"index"`
	DocValues   *bool  `json:"doc_values"`
}

type mappedSource struct {
	Enabled  *bool    `json:"enabled"`
	Mode     string   `json:"mode"`
	Includes []string `json:"includes"`
	Excludes []string `json:"excludes"`
}

type nativeSettings struct {
	Settings map[string]string `json:"settings"`
	Defaults map[string]string `json:"defaults"`
}

func (n nativeSettings) value(name string) string { return cmp.Or(n.Settings[name], n.Defaults[name]) }

type searchResponse struct {
	ScrollID string        `json:"_scroll_id"`
	TimedOut *bool         `json:"timed_out"`
	Shards   *searchShards `json:"_shards"`
	Hits     *searchHits   `json:"hits"`
}

func (s searchResponse) validate(index string) error {
	if s.TimedOut == nil || *s.TimedOut || s.Shards == nil || s.Shards.Total == nil || s.Shards.Successful == nil || s.Shards.Failed == nil || *s.Shards.Total <= 0 || *s.Shards.Failed != 0 || *s.Shards.Successful != *s.Shards.Total {
		return errors.New("elasticsearch: native search is incomplete or lacks shard acknowledgments")
	}
	if s.Hits == nil || s.Hits.Hits == nil || s.Hits.Total == nil || s.Hits.Total.Value == nil || *s.Hits.Total.Value < 0 || s.Hits.Total.Relation != "eq" {
		return errors.New("elasticsearch: native search lacks complete hits or an exact total")
	}
	for _, hit := range *s.Hits.Hits {
		if hit.Index != index {
			return errors.New("elasticsearch: native hit is outside the concrete index")
		}
	}
	return nil
}

type searchShards struct {
	Total      *int `json:"total"`
	Successful *int `json:"successful"`
	Failed     *int `json:"failed"`
}

type searchHits struct {
	Total *searchTotal `json:"total"`
	Hits  *[]searchHit `json:"hits"`
}

type searchTotal struct {
	Value    *int64 `json:"value"`
	Relation string `json:"relation"`
}

type searchRequest struct {
	Size             int                   `json:"size"`
	KNN              *nearestNeighborQuery `json:"knn,omitzero"`
	Source           bool                  `json:"_source"`
	TrackTotalHits   bool                  `json:"track_total_hits"`
	SeqNoPrimaryTerm bool                  `json:"seq_no_primary_term"`
	StoredFields     []string              `json:"stored_fields"`
	Sort             []string              `json:"sort,omitzero"`
}

type nearestNeighborQuery struct {
	Field       string          `json:"field"`
	QueryVector []float32       `json:"query_vector"`
	K           int             `json:"k"`
	Filter      *idsQueryClause `json:"filter,omitzero"`
}

type idsQueryClause struct {
	IDs idsQuery `json:"ids"`
}
type idsQuery struct {
	Values []string `json:"values"`
}

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
	SeqNo       *int64 `json:"if_seq_no,omitzero"`
	PrimaryTerm *int64 `json:"if_primary_term,omitzero"`
}

type bulkResponse struct {
	Errors *bool                              `json:"errors"`
	Items  []map[bulkOperation]bulkItemResult `json:"items"`
}

func (b bulkResponse) validate(operation bulkOperation, index string, ids []string, allowMissing bool) error {
	if b.Errors == nil || len(b.Items) != len(ids) {
		return errors.New("elasticsearch: native bulk did not acknowledge every document")
	}
	for i, item := range b.Items {
		result, present := item[operation]
		if !present || len(item) != 1 || result.ID != ids[i] || result.Index != index || result.Status == nil {
			return errors.New("elasticsearch: native bulk acknowledgment differs from the requested operation or identity")
		}
		success := *result.Status >= 200 && *result.Status < 300 && result.Error == nil
		missing := allowMissing && *result.Status == 404 && result.Error == nil
		if !success && !missing {
			return fmt.Errorf("elasticsearch: native bulk %s failed for %q: status=%d, error=%s", operation, ids[i], *result.Status, result.Error)
		}
	}
	if *b.Errors {
		return errors.New("elasticsearch: native bulk reported an unexplained failure")
	}
	return nil
}

type bulkItemResult struct {
	Index  string          `json:"_index"`
	ID     string          `json:"_id"`
	Status *int            `json:"status"`
	Error  json.RawMessage `json:"error,omitzero"`
}

type bulkPublication struct {
	body []byte
	ids  []string
}
