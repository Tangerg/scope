package opensearch

import (
	"cmp"
	"fmt"
	"strconv"
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
