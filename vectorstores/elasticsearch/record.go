package elasticsearch

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

// Elasticsearch 8.19 validates FLOAT32 squared magnitude with this tolerance.
const nativeUnitMagnitudeTolerance = float32(1e-3)

type nativeSchema struct {
	dimensions   int
	similarity   string
	resultWindow int
}

func (n nativeSchema) vector(values []float64) ([]float32, error) {
	vector := embedding.Float32Vector(values)
	if err := n.validateVector(vector); err != nil {
		return nil, err
	}
	return vector, nil
}

func (n nativeSchema) validateVector(vector []float32) error {
	if len(vector) != n.dimensions {
		return fmt.Errorf("elasticsearch: vector dimension %d differs from native dimension %d", len(vector), n.dimensions)
	}
	var magnitude float32
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("elasticsearch: vector is not finite FLOAT32")
		}
		term := float32(value * value)
		magnitude = float32(magnitude + term)
	}
	if math.IsNaN(float64(magnitude)) || math.IsInf(float64(magnitude), 0) {
		return errors.New("elasticsearch: vector magnitude is not finite FLOAT32")
	}
	if n.similarity == "cosine" && magnitude == 0 {
		return errors.New("elasticsearch: cosine vector has zero FLOAT32 magnitude")
	}
	if n.similarity == "dot_product" && math.Abs(float64(magnitude-1)) > float64(nativeUnitMagnitudeTolerance) {
		return errors.New("elasticsearch: native dot_product requires unit FLOAT32 vectors")
	}
	return nil
}

func (n nativeSchema) score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return 0, errors.New("elasticsearch: native score is invalid")
	}
	if n.similarity == "max_inner_product" {
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
	if n.similarity == "cosine" || n.similarity == "dot_product" {
		maximum += float64(nativeUnitMagnitudeTolerance) / 2
	}
	if raw > maximum {
		return 0, errors.New("elasticsearch: normalized native score exceeds its metric range")
	}
	return vectorstore.ScoreFromValue(raw), nil
}

func (n nativeSchema) decode(hit searchHit) (*document.Document, error) {
	if hit.Index == "" || hit.Routing != "" || len(hit.Fields["_routing"]) != 0 {
		return nil, errors.New("elasticsearch: native hit has no concrete index or uses custom routing")
	}
	if hit.SeqNo == nil || *hit.SeqNo < 0 || hit.PrimaryTerm == nil || *hit.PrimaryTerm <= 0 {
		return nil, errors.New("elasticsearch: native hit is missing concurrency tokens")
	}
	if len(hit.Source) == 0 {
		return nil, errors.New("elasticsearch: native hit is missing stored _source")
	}
	var record storedDocument
	if err := jsonv2.Unmarshal(hit.Source, &record, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if err := n.validateVector(record.Embedding); err != nil {
		return nil, err
	}
	var facts metadata.Map
	if err := facts.UnmarshalJSON([]byte(record.MetadataJSON)); err != nil {
		return nil, err
	}
	doc := &document.Document{ID: hit.ID, Text: record.Content, Metadata: facts}
	if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, err
	}
	if len(doc.ID) > nativeMaximumIDBytes {
		return nil, errors.New("elasticsearch: document ID exceeds native 512-byte limit")
	}
	return doc, nil
}

type storedDocument struct {
	Content      string    `json:"content"`
	Embedding    []float32 `json:"embedding"`
	MetadataJSON string    `json:"metadata_json"`
}

type searchHit struct {
	Index       string                       `json:"_index"`
	ID          string                       `json:"_id"`
	Routing     string                       `json:"_routing"`
	Fields      map[string][]json.RawMessage `json:"fields"`
	SeqNo       *int64                       `json:"_seq_no"`
	PrimaryTerm *int64                       `json:"_primary_term"`
	Score       *float64                     `json:"_score"`
	Source      json.RawMessage              `json:"_source"`
}

type scoredDocument struct {
	result *vectorstore.SearchResult
	rank   float64
}
