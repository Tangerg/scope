package opensearch

import (
	"cmp"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"

	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic"
)

const (
	knnSetting                   = "index.knn"
	derivedSourceSetting         = "index.derived_source.enabled"
	knnDerivedSourceSetting      = "index.knn.derived_source.enabled"
	defaultPipelineSetting       = "index.default_pipeline"
	finalPipelineSetting         = "index.final_pipeline"
	searchDefaultPipelineSetting = "index.search.default_pipeline"
	settingEnabled               = "true"
	settingDisabled              = "false"
	pipelineNone                 = "_none"

	fieldTypeKNNVector        = "knn_vector"
	vectorElementFloat        = "float"
	compression2x             = "2x"
	encoderScalarQuantization = "sq"
	scalarEncodingFP16        = "fp16"
	methodHNSW                = "hnsw"
	methodIVF                 = "ivf"
)

// Admission, scoring and vector validation must use the same native space types.
type nativeSimilarity string

const (
	simCosine       nativeSimilarity = "cosinesimil"
	simL2           nativeSimilarity = "l2"
	simInnerProduct nativeSimilarity = "innerproduct"
	simL1           nativeSimilarity = "l1"
	simLinf         nativeSimilarity = "linf"
)

type knnEngine string

const (
	engineLucene knnEngine = "lucene"
	engineFaiss  knnEngine = "faiss"
	engineNMSLIB knnEngine = "nmslib"
)

const nativeMaximumFP16Magnitude float32 = 65504

type engine struct{}

func (engine) Name() string { return "opensearch" }

func (engine) SettingNames() []string {
	return []string{knnSetting, derivedSourceSetting, knnDerivedSourceSetting, defaultPipelineSetting, finalPipelineSetting, searchDefaultPipelineSetting}
}

func (engine) Admit(embedding jsontext.Value, settings elastic.IndexSettings) (elastic.Schema, error) {
	var vector mappedVector
	if err := jsonv2.Unmarshal(embedding, &vector); err != nil {
		return nil, fmt.Errorf("%w: embedding: %w", elastic.ErrIncompatibleIndex, err)
	}
	if vector.Type != fieldTypeKNNVector || vector.Dimensions <= 0 || vector.Dimensions > nativeMaximumDimensions || vector.DataType != "" && vector.DataType != vectorElementFloat || vector.ModelID != "" || vector.Method == nil || vector.Method.Engine == "" {
		return nil, fmt.Errorf("%w: requires FLOAT32 knn_vector with declared dimensions and native method engine", elastic.ErrIncompatibleIndex)
	}
	if vector.SpaceType != "" && vector.Method.SpaceType != "" && vector.SpaceType != vector.Method.SpaceType {
		return nil, fmt.Errorf("%w: native field and method space disagree", elastic.ErrIncompatibleIndex)
	}
	schema := nativeSchema{
		dimensions:       vector.Dimensions,
		similarity:       cmp.Or(nativeSimilarity(vector.SpaceType), nativeSimilarity(vector.Method.SpaceType), simL2),
		engine:           vector.Method.Engine,
		requireFP16Range: vector.requiresFP16Range(),
	}
	if err := schema.admitMethod(vector.Method.Name); err != nil {
		return nil, err
	}
	if settings.Value(knnSetting) != settingEnabled {
		return nil, fmt.Errorf("%w: native KNN must be enabled", elastic.ErrIncompatibleIndex)
	}
	for _, name := range []string{derivedSourceSetting, knnDerivedSourceSetting} {
		if settings.Value(name) != settingDisabled {
			return nil, fmt.Errorf("%w: derived source must be disabled", elastic.ErrIncompatibleIndex)
		}
	}
	for _, name := range []string{defaultPipelineSetting, finalPipelineSetting, searchDefaultPipelineSetting} {
		if settings.Value(name) != pipelineNone {
			return nil, fmt.Errorf("%w: native pipeline can change document facts or results", elastic.ErrIncompatibleIndex)
		}
	}
	return schema, nil
}

type mappedVector struct {
	Type             string        `json:"type"`
	Dimensions       int           `json:"dimension"`
	SpaceType        string        `json:"space_type"`
	DataType         string        `json:"data_type"`
	ModelID          string        `json:"model_id"`
	CompressionLevel string        `json:"compression_level"`
	Method           *nativeMethod `json:"method,omitzero"`
}

// requiresFP16Range reports whether Faiss stores the vector as unclipped FP16,
// which rejects coordinates outside the FP16 range instead of saturating them.
func (m mappedVector) requiresFP16Range() bool {
	if m.Method.Engine != engineFaiss {
		return false
	}
	encoder := m.Method.Parameters.Encoder
	if encoder != nil {
		fp16 := encoder.Name == encoderScalarQuantization && cmp.Or(encoder.Parameters.Type, scalarEncodingFP16) == scalarEncodingFP16
		return (fp16 || m.CompressionLevel == compression2x) && !encoder.Parameters.Clip
	}
	return m.CompressionLevel == compression2x
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

type nativeSchema struct {
	dimensions       int
	similarity       nativeSimilarity
	engine           knnEngine
	requireFP16Range bool
}

func (n nativeSchema) admitMethod(method string) error {
	switch n.similarity {
	case simCosine, simL2, simInnerProduct, simL1, simLinf:
	default:
		return fmt.Errorf("%w: unsupported native space %q", elastic.ErrIncompatibleIndex, n.similarity)
	}
	switch n.engine {
	case engineLucene, engineFaiss:
		if n.similarity == simL1 || n.similarity == simLinf {
			return fmt.Errorf("%w: native engine does not support space %q", elastic.ErrIncompatibleIndex, n.similarity)
		}
	case engineNMSLIB:
	default:
		return fmt.Errorf("%w: unsupported native engine %q", elastic.ErrIncompatibleIndex, n.engine)
	}
	if method != methodHNSW && (method != methodIVF || n.engine != engineFaiss) {
		return fmt.Errorf("%w: unsupported native ANN method", elastic.ErrIncompatibleIndex)
	}
	return nil
}

func (n nativeSchema) Dimensions() int { return n.dimensions }

func (n nativeSchema) ValidateIndexedVector(vector []float32) error {
	if err := n.ValidateQueryVector(vector); err != nil {
		return err
	}
	if n.requireFP16Range {
		for _, value := range vector {
			if value < -nativeMaximumFP16Magnitude || value > nativeMaximumFP16Magnitude {
				return fmt.Errorf("opensearch: native Faiss FP16 encoding requires coordinates in [%g,%g]", -nativeMaximumFP16Magnitude, nativeMaximumFP16Magnitude)
			}
		}
	}
	return nil
}

func (n nativeSchema) ValidateQueryVector(vector []float32) error {
	if err := corevector.Coordinates[float32](vector).Validate(); err != nil {
		return err
	}
	if len(vector) != n.dimensions {
		return fmt.Errorf("opensearch: vector dimension %d differs from native dimension %d", len(vector), n.dimensions)
	}
	if n.similarity == simCosine {
		return corevector.Coordinates[float32](vector).ValidateDirection()
	}
	return nil
}

func (n nativeSchema) Score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return 0, errors.New("opensearch: native score is invalid")
	}
	if n.similarity == simInnerProduct {
		if raw == 0 {
			return 0, errors.New("opensearch: native inner product score is zero")
		}
		product := raw - 1
		if raw < 1 {
			product = 1 - 1/raw
		}
		return vectorstore.ScoreFromInnerProduct(product), nil
	}
	if n.similarity != simCosine && raw > 1 {
		return 0, errors.New("opensearch: native distance score exceeds its metric range")
	}
	// Native cosine scoring clamps only the lower bound; Core owns normalization
	// of FLOAT32 rounding above one.
	return vectorstore.ScoreFromValue(raw), nil
}

func (n nativeSchema) CheckFilteredSearch() error {
	if n.engine == engineNMSLIB {
		return fmt.Errorf("%w: opensearch: filtered KNN requires Lucene or Faiss", errors.ErrUnsupported)
	}
	return nil
}

func (nativeSchema) SearchSyntax(neighbors *elastic.Neighbors) (jsontext.Value, error) {
	syntax := searchSyntax{Source: true}
	if neighbors != nil {
		knn := &nearestNeighborQuery{Vector: neighbors.Vector, K: neighbors.K}
		if neighbors.IDs != nil {
			knn.Filter = &idsQueryClause{IDs: idsQuery{Values: neighbors.IDs}}
		}
		syntax.Query = &knnQuery{KNN: map[string]*nearestNeighborQuery{elastic.EmbeddingField: knn}}
	}
	return jsonv2.Marshal(syntax)
}

type searchSyntax struct {
	Source bool      `json:"_source"`
	Query  *knnQuery `json:"query,omitzero"`
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
