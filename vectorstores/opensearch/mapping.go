package opensearch

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"github.com/Tangerg/scope/core/vectorstore"
)

type mappedIndex struct {
	Mappings nativeMapping `json:"mappings"`
}

type nativeMapping struct {
	Meta struct {
		Space string `json:"scope_vector_space"`
	} `json:"_meta"`
	Dynamic    string                 `json:"dynamic"`
	Properties map[string]mappedField `json:"properties"`
	Source     mappedSource           `json:"_source"`
	Routing    struct {
		Required bool `json:"required"`
	} `json:"_routing"`
	Runtime          map[string]any `json:"runtime"`
	Derived          map[string]any `json:"derived"`
	DynamicTemplates []any          `json:"dynamic_templates"`
}

func (n nativeMapping) schema(settings nativeSettings) (nativeSchema, error) {
	const (
		dynamicMappingStrict      = "strict"
		sourceModeStored          = "stored"
		fieldTypeText             = "text"
		fieldTypeKeyword          = "keyword"
		fieldTypeKNNVector        = "knn_vector"
		vectorElementFloat        = "float"
		pipelineNone              = "_none"
		compression2x             = "2x"
		encoderScalarQuantization = "sq"
		scalarEncodingFP16        = "fp16"
		methodHNSW                = "hnsw"
		methodIVF                 = "ivf"
	)
	var schema nativeSchema
	if n.Dynamic != dynamicMappingStrict || len(n.Properties) != 3 || n.Routing.Required || len(n.Runtime) > 0 || len(n.DynamicTemplates) > 0 || len(n.Derived) > 0 {
		return schema, fmt.Errorf("%w: requires exactly the current strict properties without routing or dynamic rules", ErrIncompatibleIndex)
	}
	if n.Source.Enabled != nil && !*n.Source.Enabled || len(n.Source.Includes) > 0 || len(n.Source.Excludes) > 0 || n.Source.Mode != "" && n.Source.Mode != sourceModeStored {
		return schema, fmt.Errorf("%w: requires complete stored _source", ErrIncompatibleIndex)
	}
	if field := n.Properties[contentField]; field.Type != fieldTypeText {
		return schema, fmt.Errorf("%w: content must be TEXT", ErrIncompatibleIndex)
	}
	if field := n.Properties[metadataField]; field.Type != fieldTypeKeyword || field.Index == nil || *field.Index || field.DocValues == nil || *field.DocValues {
		return schema, fmt.Errorf("%w: metadata_json must be an unindexed KEYWORD without doc values", ErrIncompatibleIndex)
	}
	vector := n.Properties[embeddingField]
	if vector.Type != fieldTypeKNNVector || vector.Dimensions <= 0 || vector.Dimensions > nativeMaximumDimensions || vector.DataType != "" && vector.DataType != vectorElementFloat || vector.ModelID != "" || vector.Method == nil || vector.Method.Engine == "" {
		return schema, fmt.Errorf("%w: requires FLOAT32 knn_vector with declared dimensions and native method engine", ErrIncompatibleIndex)
	}
	if vector.SpaceType != "" && vector.Method.SpaceType != "" && vector.SpaceType != vector.Method.SpaceType {
		return schema, fmt.Errorf("%w: native field and method space disagree", ErrIncompatibleIndex)
	}
	schema.dimensions = vector.Dimensions
	schema.similarity = cmp.Or(nativeSimilarity(vector.SpaceType), nativeSimilarity(vector.Method.SpaceType), simL2)
	schema.engine = vector.Method.Engine
	encoder := vector.Method.Parameters.Encoder
	schema.requireFP16Range = schema.engine == engineFaiss && (vector.CompressionLevel == compression2x || encoder != nil && encoder.Name == encoderScalarQuantization && cmp.Or(encoder.Parameters.Type, scalarEncodingFP16) == scalarEncodingFP16)
	if schema.requireFP16Range && encoder != nil {
		schema.requireFP16Range = !encoder.Parameters.Clip
	}
	switch schema.similarity {
	case simCosine, simL2, simInnerProduct, simL1, simLinf:
	default:
		return schema, fmt.Errorf("%w: unsupported native space %q", ErrIncompatibleIndex, schema.similarity)
	}
	switch schema.engine {
	case engineLucene, engineFaiss:
		if schema.similarity == simL1 || schema.similarity == simLinf {
			return schema, fmt.Errorf("%w: native engine does not support space %q", ErrIncompatibleIndex, schema.similarity)
		}
	case engineNMSLIB:
	default:
		return schema, fmt.Errorf("%w: unsupported native engine %q", ErrIncompatibleIndex, schema.engine)
	}
	if vector.Method.Name != methodHNSW && (vector.Method.Name != methodIVF || schema.engine != engineFaiss) {
		return schema, fmt.Errorf("%w: unsupported native ANN method", ErrIncompatibleIndex)
	}
	if settings.value("index.knn") != "true" {
		return schema, fmt.Errorf("%w: native KNN must be enabled", ErrIncompatibleIndex)
	}
	for _, name := range []string{"index.derived_source.enabled", "index.knn.derived_source.enabled"} {
		if settings.value(name) != "false" {
			return schema, fmt.Errorf("%w: derived source must be disabled", ErrIncompatibleIndex)
		}
	}
	for _, name := range []string{"index.default_pipeline", "index.final_pipeline", "index.search.default_pipeline"} {
		if settings.value(name) != pipelineNone {
			return schema, fmt.Errorf("%w: native pipeline can change document facts or results", ErrIncompatibleIndex)
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
	Type             string        `json:"type"`
	Dimensions       int           `json:"dimension"`
	SpaceType        string        `json:"space_type"`
	DataType         string        `json:"data_type"`
	ModelID          string        `json:"model_id"`
	CompressionLevel string        `json:"compression_level"`
	Method           *nativeMethod `json:"method,omitzero"`
	Index            *bool         `json:"index"`
	DocValues        *bool         `json:"doc_values"`
}

type nativeMethod struct {
	Name       string    `json:"name"`
	Engine     knnEngine `json:"engine"`
	SpaceType  string    `json:"space_type"`
	Parameters struct {
		Encoder *nativeEncoder `json:"encoder,omitzero"`
	} `json:"parameters"`
}

type nativeEncoder struct {
	Name       string `json:"name"`
	Parameters struct {
		Type string `json:"type"`
		Clip bool   `json:"clip"`
	} `json:"parameters"`
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
	ScrollID        string        `json:"_scroll_id"`
	TerminatedEarly *bool         `json:"terminated_early"`
	TimedOut        *bool         `json:"timed_out"`
	Shards          *searchShards `json:"_shards"`
	Hits            *searchHits   `json:"hits"`
}

func (s searchResponse) validate(index string) error {
	if s.TerminatedEarly != nil && *s.TerminatedEarly || s.TimedOut == nil || *s.TimedOut || s.Shards == nil || s.Shards.Total == nil || s.Shards.Successful == nil || s.Shards.Failed == nil || *s.Shards.Total <= 0 || *s.Shards.Failed != 0 || *s.Shards.Successful != *s.Shards.Total {
		return errors.New("opensearch: native search is incomplete or lacks shard acknowledgments")
	}
	if s.Hits == nil || s.Hits.Hits == nil || s.Hits.Total == nil || s.Hits.Total.Value == nil || *s.Hits.Total.Value < 0 || s.Hits.Total.Relation != string(opensearchapi.SearchTotalHitsRelationEq) {
		return errors.New("opensearch: native search lacks complete hits or an exact total")
	}
	for _, hit := range *s.Hits.Hits {
		if hit.Index != index {
			return errors.New("opensearch: native hit is outside the concrete index")
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
	Size             int       `json:"size"`
	Query            *knnQuery `json:"query,omitzero"`
	Source           bool      `json:"_source"`
	TrackTotalHits   bool      `json:"track_total_hits"`
	SeqNoPrimaryTerm bool      `json:"seq_no_primary_term"`
	StoredFields     []string  `json:"stored_fields"`
	Sort             []string  `json:"sort,omitzero"`
}

type knnQuery struct {
	KNN map[string]*nearestNeighborQuery `json:"knn"`
}

type nearestNeighborQuery struct {
	Vector []float32       `json:"vector"`
	K      int             `json:"k"`
	Filter *idsQueryClause `json:"filter,omitzero"`
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
		return errors.New("opensearch: native bulk did not acknowledge every document")
	}
	for i, item := range b.Items {
		result, present := item[operation]
		if !present || len(item) != 1 || result.ID != ids[i] || result.Index != index || result.Status == nil {
			return errors.New("opensearch: native bulk acknowledgment differs from the requested operation or identity")
		}
		success := *result.Status >= 200 && *result.Status < 300 && result.Error == nil
		missing := allowMissing && *result.Status == 404 && result.Error == nil
		if !success && !missing {
			return fmt.Errorf("opensearch: native bulk %s failed for %q: status=%d, error=%s", operation, ids[i], *result.Status, result.Error)
		}
	}
	if *b.Errors {
		return errors.New("opensearch: native bulk reported an unexplained failure")
	}
	return nil
}

// Only individually attributable item acknowledgments can advance per-ID
// knowledge. A contradictory complete summary is not an alternative authority.
func (b bulkResponse) writeFailure(index string, ids []string, cause error) *vectorstore.WriteError {
	var w vectorstore.WriteOutcome
	if b.Errors == nil || len(b.Items) > len(ids) {
		w.Unconfirmed = ids
		return vectorstore.NewWriteError(cause, w)
	}
	bad := false
	for i, id := range ids {
		if i >= len(b.Items) {
			w.Unconfirmed = append(w.Unconfirmed, id)
			continue
		}
		item := b.Items[i]
		r, ok := item[bulkOperationIndex]
		if !ok || len(item) != 1 || r.ID != id || r.Index != index || r.Status == nil {
			w.Unconfirmed = append(w.Unconfirmed, id)
			continue
		}
		switch {
		case *r.Status >= 200 && *r.Status < 300 && r.Error == nil:
			w.Acknowledged = append(w.Acknowledged, id)
		case *r.Status >= 400 && *r.Status < 500 && r.Error != nil:
			w.Rejected = append(w.Rejected, id)
			bad = true
		default:
			w.Unconfirmed = append(w.Unconfirmed, id)
			bad = true
		}
	}
	if !*b.Errors && bad || *b.Errors && len(w.Acknowledged) == len(ids) {
		return vectorstore.NewWriteError(cause, vectorstore.WriteOutcome{Unconfirmed: ids})
	}
	return vectorstore.NewWriteError(cause, w)
}

type bulkItemResult struct {
	Index  string          `json:"_index"`
	ID     string          `json:"_id"`
	Status *int            `json:"status"`
	Error  json.RawMessage `json:"error,omitzero"`
}
