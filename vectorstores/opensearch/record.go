package opensearch

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

type nativeSchema struct {
	dimensions       int
	similarity       string
	engine           string
	requireFP16Range bool
	resultWindow     int
}

func (n nativeSchema) vector(values []float64) ([]float32, error) {
	vector := embedding.Float32Vector(values)
	if err := n.validateVector(vector); err != nil {
		return nil, err
	}
	return vector, nil
}

func (n nativeSchema) indexVector(values []float64) ([]float32, error) {
	vector := embedding.Float32Vector(values)
	if err := n.validateIndexedVector(vector); err != nil {
		return nil, err
	}
	return vector, nil
}

func (n nativeSchema) validateIndexedVector(vector []float32) error {
	if err := n.validateVector(vector); err != nil {
		return err
	}
	if n.requireFP16Range {
		for _, value := range vector {
			if value < -65504 || value > 65504 {
				return errors.New("opensearch: native Faiss FP16 encoding requires coordinates in [-65504,65504]")
			}
		}
	}
	return nil
}

func (n nativeSchema) validateVector(vector []float32) error {
	if len(vector) != n.dimensions {
		return fmt.Errorf("opensearch: vector dimension %d differs from native dimension %d", len(vector), n.dimensions)
	}
	zero := true
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("opensearch: vector is not finite FLOAT32")
		}
		zero = zero && value == 0
	}
	if n.similarity == "cosinesimil" && zero {
		return errors.New("opensearch: native cosine space does not accept a zero FLOAT32 vector")
	}

	return nil
}

func (n nativeSchema) score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return 0, errors.New("opensearch: native score is invalid")
	}
	if n.similarity == "innerproduct" {
		if raw == 0 {
			return 0, errors.New("opensearch: native inner product score is zero")
		}
		product := raw - 1
		if raw < 1 {
			product = 1 - 1/raw
		}
		return vectorstore.ScoreFromInnerProduct(product), nil
	}
	if n.similarity != "cosinesimil" && raw > 1 {
		return 0, errors.New("opensearch: native distance score exceeds its metric range")
	}
	// Native cosine scoring clamps only the lower bound; Core owns normalization
	// of FLOAT32 rounding above one.
	return vectorstore.ScoreFromValue(raw), nil
}

func (n nativeSchema) decode(hit searchHit) (*document.Document, error) {
	if hit.Index == "" || hit.Routing != "" || len(hit.Fields["_routing"]) != 0 {
		return nil, errors.New("opensearch: native hit has no concrete index or uses custom routing")
	}
	if hit.SeqNo == nil || *hit.SeqNo < 0 || hit.PrimaryTerm == nil || *hit.PrimaryTerm <= 0 {
		return nil, errors.New("opensearch: native hit is missing concurrency tokens")
	}
	if len(hit.Source) == 0 {
		return nil, errors.New("opensearch: native hit is missing stored _source")
	}
	var record storedDocument
	if err := jsonv2.Unmarshal(hit.Source, &record, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if err := n.validateIndexedVector(record.Embedding); err != nil {
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
		return nil, errors.New("opensearch: document ID exceeds native 512-byte limit")
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
