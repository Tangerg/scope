package redis

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type nativeSchema struct {
	prefix     string
	dimensions int
	metric     string
}

func (n nativeSchema) vector(values []float64) ([]byte, error) {
	vector := embedding.Float32Vector(values)
	if err := n.validateVector(vector); err != nil {
		return nil, err
	}
	return float32sToBytes(vector), nil
}

func (n nativeSchema) validateVector(vector []float32) error {
	if len(vector) != n.dimensions {
		return fmt.Errorf("redis: vector dimension %d differs from native dimension %d", len(vector), n.dimensions)
	}
	nonzero := false
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("redis: vector is not finite float32")
		}
		nonzero = nonzero || value != 0
	}
	if n.metric == "COSINE" && !nonzero {
		return errors.New("redis: cosine vector became all zero in float32")
	}
	return nil
}

func (n nativeSchema) decode(key string, fields map[string]string) (*document.Document, error) {
	if !strings.HasPrefix(key, n.prefix) || len(key) == len(n.prefix) {
		return nil, errors.New("redis: native key is outside the document namespace")
	}
	if len(fields) != 3 {
		return nil, errors.New("redis: HASH must contain exactly the current document fields")
	}
	text, textOK := fields[contentField]
	facts, factsOK := fields[metadataField]
	blob, vectorOK := fields[embeddingField]
	if !textOK || !factsOK || !vectorOK {
		return nil, errors.New("redis: HASH is missing required current fields")
	}
	if len(blob)%float32ByteWidth != 0 {
		return nil, errors.New("redis: vector blob is not a complete FLOAT32 sequence")
	}
	vector := make([]float32, len(blob)/float32ByteWidth)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32([]byte(blob[i*float32ByteWidth : (i+1)*float32ByteWidth])))
	}
	if err := n.validateVector(vector); err != nil {
		return nil, err
	}
	var values metadata.Map
	if err := values.UnmarshalJSON([]byte(facts)); err != nil {
		return nil, err
	}
	doc := &document.Document{ID: strings.TrimPrefix(key, n.prefix), Text: text, Metadata: values}
	if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, err
	}
	return doc, nil
}

func (n nativeSchema) score(distance float64) (vectorstore.Score, error) {
	if math.IsNaN(distance) || math.IsInf(distance, 0) {
		return 0, errors.New("redis: native distance is not finite")
	}
	switch n.metric {
	case "COSINE":
		if distance < 0 || distance > 2 {
			return 0, errors.New("redis: native cosine distance is outside [0,2]")
		}
		return vectorstore.ScoreFromCosineDistance(distance), nil
	case "L2":
		if distance < 0 {
			return 0, errors.New("redis: native L2 distance is negative")
		}
		return vectorstore.ScoreFromDistance(distance), nil
	case "IP":
		return vectorstore.ScoreFromOneMinusInnerProductDistance(distance), nil
	default:
		return 0, errors.New("redis: native distance metric is unsupported")
	}
}

type hashRecord struct {
	key, content, metadata string
	vector                 []byte
}
