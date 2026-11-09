package elasticsearch

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/densevectorelementtype"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/dynamicmapping"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/fieldtype"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/sourcemode"
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
	DynamicTemplates []any          `json:"dynamic_templates"`
}

func (n nativeMapping) schema(settings nativeSettings) (nativeSchema, error) {
	const pipelineNone = "_none"
	var schema nativeSchema
	if n.Dynamic != dynamicmapping.Strict.String() || len(n.Properties) != 3 || n.Routing.Required || len(n.Runtime) > 0 || len(n.DynamicTemplates) > 0 {
		return schema, fmt.Errorf("%w: requires exactly the current strict properties without routing or dynamic rules", ErrIncompatibleIndex)
	}
	if n.Source.Enabled != nil && !*n.Source.Enabled || len(n.Source.Includes) > 0 || len(n.Source.Excludes) > 0 || n.Source.Mode != "" && n.Source.Mode != sourcemode.Stored.String() {
		return schema, fmt.Errorf("%w: requires complete stored _source", ErrIncompatibleIndex)
	}
	if field := n.Properties[contentField]; field.Type != fieldtype.Text.String() {
		return schema, fmt.Errorf("%w: content must be TEXT", ErrIncompatibleIndex)
	}
	if field := n.Properties[metadataField]; field.Type != fieldtype.Keyword.String() || field.Index == nil || *field.Index || field.DocValues == nil || *field.DocValues {
		return schema, fmt.Errorf("%w: metadata_json must be an unindexed KEYWORD without doc values", ErrIncompatibleIndex)
	}
	vector := n.Properties[embeddingField]
	if vector.Type != fieldtype.Densevector.String() || vector.Dimensions <= 0 || vector.Dimensions > nativeMaximumDimensions || vector.Index != nil && !*vector.Index || vector.ElementType != "" && vector.ElementType != densevectorelementtype.Float.String() {
		return schema, fmt.Errorf("%w: requires indexed FLOAT32 dense_vector with a declared dimension", ErrIncompatibleIndex)
	}
	schema.dimensions = vector.Dimensions
	schema.similarity = cmp.Or(nativeSimilarity(vector.Similarity), simCosine)
	switch schema.similarity {
	case simCosine, simL2Norm, simDotProduct, simMaxInnerProduct:
	default:
		return schema, fmt.Errorf("%w: unsupported native similarity %q", ErrIncompatibleIndex, schema.similarity)
	}
	if !strings.EqualFold(settings.value("index.mapping.source.mode"), sourcemode.Stored.String()) {
		return schema, fmt.Errorf("%w: source mode must be stored", ErrIncompatibleIndex)
	}
	for _, name := range []string{"index.default_pipeline", "index.final_pipeline"} {
		if value := settings.value(name); value != pipelineNone {
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

type searchRequest struct {
	Size             int                   `json:"size"`
	KNN              *nearestNeighborQuery `json:"knn,omitzero"`
	Source           sourceProjection      `json:"_source"`
	TrackTotalHits   bool                  `json:"track_total_hits"`
	SeqNoPrimaryTerm bool                  `json:"seq_no_primary_term"`
	StoredFields     []string              `json:"stored_fields"`
	Sort             []string              `json:"sort,omitzero"`
}

// A boolean _source request can omit vectors even from a complete stored source.
// The zero projection explicitly includes the vector required by record decoding.
type sourceProjection struct {
	ExcludeVectors bool `json:"exclude_vectors"`
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
