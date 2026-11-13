package elasticsearch

import (
	"cmp"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/densevectorelementtype"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/fieldtype"
	"github.com/elastic/go-elasticsearch/v9/typedapi/types/enums/sourcemode"

	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic"
)

const (
	sourceModeSetting      = "index.mapping.source.mode"
	defaultPipelineSetting = "index.default_pipeline"
	finalPipelineSetting   = "index.final_pipeline"
	pipelineNone           = "_none"
)

// Elasticsearch 8.19 validates FLOAT32 squared magnitude with this tolerance.
const nativeUnitMagnitudeTolerance = float32(1e-3)

// nativeSimilarity is the dense_vector similarity parsed from the mapping;
// score and vector validation select behavior from it, so the accepted set
// and the branches must name the same values.
type nativeSimilarity string

const (
	simCosine          nativeSimilarity = "cosine"
	simL2Norm          nativeSimilarity = "l2_norm"
	simDotProduct      nativeSimilarity = "dot_product"
	simMaxInnerProduct nativeSimilarity = "max_inner_product"
)

type engine struct{}

func (engine) Name() string { return "elasticsearch" }

func (engine) SettingNames() []string {
	return []string{sourceModeSetting, defaultPipelineSetting, finalPipelineSetting}
}

func (engine) Admit(embedding jsontext.Value, settings elastic.IndexSettings) (elastic.Schema, error) {
	var vector mappedVector
	if err := jsonv2.Unmarshal(embedding, &vector); err != nil {
		return nil, fmt.Errorf("%w: embedding: %w", elastic.ErrIncompatibleIndex, err)
	}
	if vector.Type != fieldtype.Densevector.String() || vector.Dimensions <= 0 || vector.Dimensions > nativeMaximumDimensions || vector.Index != nil && !*vector.Index || vector.ElementType != "" && vector.ElementType != densevectorelementtype.Float.String() {
		return nil, fmt.Errorf("%w: requires indexed FLOAT32 dense_vector with a declared dimension", elastic.ErrIncompatibleIndex)
	}
	schema := nativeSchema{dimensions: vector.Dimensions, similarity: cmp.Or(nativeSimilarity(vector.Similarity), simCosine)}
	switch schema.similarity {
	case simCosine, simL2Norm, simDotProduct, simMaxInnerProduct:
	default:
		return nil, fmt.Errorf("%w: unsupported native similarity %q", elastic.ErrIncompatibleIndex, schema.similarity)
	}
	if !strings.EqualFold(settings.Value(sourceModeSetting), sourcemode.Stored.String()) {
		return nil, fmt.Errorf("%w: source mode must be stored", elastic.ErrIncompatibleIndex)
	}
	for _, name := range []string{defaultPipelineSetting, finalPipelineSetting} {
		if value := settings.Value(name); value != pipelineNone {
			return nil, fmt.Errorf("%w: native ingest pipeline %q can change document facts", elastic.ErrIncompatibleIndex, value)
		}
	}
	return schema, nil
}

type mappedVector struct {
	Type        string `json:"type"`
	Dimensions  int    `json:"dims"`
	Similarity  string `json:"similarity"`
	ElementType string `json:"element_type"`
	Index       *bool  `json:"index"`
}

type nativeSchema struct {
	dimensions int
	similarity nativeSimilarity
}

func (n nativeSchema) Dimensions() int { return n.dimensions }

func (n nativeSchema) ValidateIndexedVector(vector []float32) error { return n.validateVector(vector) }

func (n nativeSchema) ValidateQueryVector(vector []float32) error { return n.validateVector(vector) }

func (n nativeSchema) validateVector(vector []float32) error {
	if err := corevector.Coordinates[float32](vector).Validate(); err != nil {
		return err
	}
	if len(vector) != n.dimensions {
		return fmt.Errorf("elasticsearch: vector dimension %d differs from native dimension %d", len(vector), n.dimensions)
	}
	var magnitude float32
	for _, value := range vector {
		term := float32(value * value)
		magnitude = float32(magnitude + term)
	}
	if math.IsNaN(float64(magnitude)) || math.IsInf(float64(magnitude), 0) {
		return errors.New("elasticsearch: vector magnitude is not finite FLOAT32")
	}
	if n.similarity == simCosine && magnitude == 0 {
		return errors.New("elasticsearch: cosine vector has zero FLOAT32 magnitude")
	}
	if n.similarity == simDotProduct && math.Abs(float64(magnitude-1)) > float64(nativeUnitMagnitudeTolerance) {
		return errors.New("elasticsearch: native dot_product requires unit FLOAT32 vectors")
	}
	return nil
}

func (n nativeSchema) Score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return 0, errors.New("elasticsearch: native score is invalid")
	}
	if n.similarity == simMaxInnerProduct {
		if raw == 0 {
			return 0, errors.New("elasticsearch: native inner product score is zero")
		}
		product := raw - 1
		if raw < 1 {
			product = 1 - 1/raw
		}
		return vectorstore.ScoreFromInnerProduct(product), nil
	}
	maximum := float64(1)
	if n.similarity == simCosine || n.similarity == simDotProduct {
		maximum += float64(nativeUnitMagnitudeTolerance) / 2
	}
	if raw > maximum {
		return 0, errors.New("elasticsearch: normalized native score exceeds its metric range")
	}
	return vectorstore.ScoreFromValue(raw), nil
}

func (nativeSchema) CheckFilteredSearch() error { return nil }

func (nativeSchema) SearchSyntax(neighbors *elastic.Neighbors) (jsontext.Value, error) {
	syntax := searchSyntax{}
	if neighbors != nil {
		syntax.KNN = &nearestNeighborQuery{Field: elastic.EmbeddingField, QueryVector: neighbors.Vector, K: neighbors.K}
		if neighbors.IDs != nil {
			syntax.KNN.Filter = &idsQueryClause{IDs: idsQuery{Values: neighbors.IDs}}
		}
	}
	return jsonv2.Marshal(syntax)
}

type searchSyntax struct {
	Source sourceProjection      `json:"_source"`
	KNN    *nearestNeighborQuery `json:"knn,omitzero"`
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
