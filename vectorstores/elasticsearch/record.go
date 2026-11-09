package elasticsearch

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"

	"github.com/Tangerg/scope/vectorstores/protocol/elastic"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Elasticsearch 8.19 validates FLOAT32 squared magnitude with this tolerance.
const nativeUnitMagnitudeTolerance = float32(1e-3)

// nativeSimilarity is the Elasticsearch dense_vector similarity parsed from the
// mapping; score and vector validation select behavior from it, so the accepted set
// and the branches must name the same values.
type nativeSimilarity string

const (
	simCosine          nativeSimilarity = "cosine"
	simL2Norm          nativeSimilarity = "l2_norm"
	simDotProduct      nativeSimilarity = "dot_product"
	simMaxInnerProduct nativeSimilarity = "max_inner_product"
)

type nativeSchema struct {
	dimensions   int
	similarity   nativeSimilarity
	resultWindow int
}

func (n nativeSchema) vector(values []float64) ([]float32, error) {
	vector := corevector.Float32(values)
	if err := n.validateVector(vector); err != nil {
		return nil, err
	}
	return vector, nil
}

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

func (n nativeSchema) score(raw float64) (vectorstore.Score, error) {
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

func (n nativeSchema) decode(hit elastic.Hit) (*document.Document, error) {
	if err := hit.Validate(); err != nil {
		return nil, err
	}
	var record storedDocument
	if err := jsonv2.Unmarshal(hit.Source, &record, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if err := n.validateVector(record.Embedding); err != nil {
		return nil, err
	}
	doc := new(document.Document)
	if err := doc.UnmarshalStoredMetadata([]byte(record.MetadataJSON)); err != nil {
		return nil, err
	}
	doc.ID, doc.Text = hit.ID, record.Content
	if err := vectorstore.ValidateDocument(doc); err != nil {
		return nil, err
	}

	return doc, nil
}

type storedDocument struct {
	Content      string                          `json:"content"`
	Embedding    corevector.Coordinates[float32] `json:"embedding"`
	MetadataJSON string                          `json:"metadata_json"`
}

type scoredDocument struct {
	result *vectorstore.SearchResult
	rank   float64
}
