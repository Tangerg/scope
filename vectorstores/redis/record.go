package redis

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

// nativeMetric is the Redis vector distance metric parsed from FT.INFO; score
// selects its formula from it, so the accepted set and the scoring branches must
// name the same values.
type nativeMetric string

const (
	metricCosine nativeMetric = "COSINE"
	metricL2     nativeMetric = "L2"
	metricIP     nativeMetric = "IP"
)

type nativeSchema struct {
	indexedTerms bool
	prefix       string
	dimensions   int
	metric       nativeMetric
}

func (n nativeMetric) valid() bool {
	return n == metricCosine || n == metricL2 || n == metricIP
}

// readAttributes admits exactly the fields this store writes: an optional
// terms TAG, the content TEXT, and one FLOAT32 embedding, which sets the
// dimension and metric.
func (n *nativeSchema) readAttributes(attributes []goredis.FTAttribute) error {
	vectorFound := false
	for _, attribute := range attributes {
		if attribute.Identifier != attribute.Attribute {
			return fmt.Errorf("%w: native aliases are unsupported", ErrIncompatibleIndex)
		}
		switch {
		case attribute.Attribute == termsField && attribute.Type == goredis.SearchFieldTypeTag.String():
			n.indexedTerms = true
		case attribute.Attribute == contentField && attribute.Type == goredis.SearchFieldTypeText.String():
		case vectorFound || attribute.Attribute != embeddingField || attribute.Type != goredis.SearchFieldTypeVector.String() || attribute.DataType != vectorElementTypeFloat32 || attribute.Dim <= 0:
			return fmt.Errorf("%w: requires one FLOAT32 embedding field", ErrIncompatibleIndex)
		case !nativeMetric(attribute.DistanceMetric).valid():
			return fmt.Errorf("%w: unsupported distance %q", ErrIncompatibleIndex, attribute.DistanceMetric)
		default:
			n.dimensions, n.metric, vectorFound = attribute.Dim, nativeMetric(attribute.DistanceMetric), true
		}
	}
	if !vectorFound {
		return fmt.Errorf("%w: embedding field is missing", ErrIncompatibleIndex)
	}
	return nil
}

func (n nativeSchema) vector(values []float64) ([]byte, error) {
	if err := vectorstore.RequireWidth(values, n.dimensions); err != nil {
		return nil, err
	}
	vector := corevector.Float32(values)
	if err := n.validateVector(vector); err != nil {
		return nil, err
	}
	return float32sToBytes(vector), nil
}

func (n nativeSchema) validateVector(vector []float32) error {
	if err := corevector.Coordinates[float32](vector).Validate(); err != nil {
		return err
	}
	if len(vector) != n.dimensions {
		return fmt.Errorf("redis: vector dimension %d differs from native dimension %d", len(vector), n.dimensions)
	}
	if n.metric == metricCosine {
		return corevector.Coordinates[float32](vector).ValidateDirection()
	}
	return nil
}

func (n nativeSchema) decode(key string, fields map[string]string) (*document.Document, error) {
	if !strings.HasPrefix(key, n.prefix) || len(key) == len(n.prefix) {
		return nil, errors.New("redis: native key is outside the document namespace")
	}
	if len(fields) != 4 {
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
	doc := new(document.Document)
	if err := doc.UnmarshalStoredMetadata([]byte(facts)); err != nil {
		return nil, err
	}
	expected, err := tagValues(doc.Metadata)
	if err != nil {
		return nil, err
	}
	if got, ok := fields[termsField]; !ok || got != expected {
		return nil, errors.New("redis: invalid derived metadata terms")
	}
	doc.ID, doc.Text = strings.TrimPrefix(key, n.prefix), text
	if err := vectorstore.ValidateDocument(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (n nativeSchema) score(distance float64) (vectorstore.Score, error) {
	if math.IsNaN(distance) || math.IsInf(distance, 0) {
		return 0, errors.New("redis: native distance is not finite")
	}
	switch n.metric {
	case metricCosine:
		score := vectorstore.ScoreFromCosineDistance(distance)
		return score, score.Validate()
	case metricL2:
		if distance < 0 {
			return 0, errors.New("redis: native L2 distance is negative")
		}
		return vectorstore.ScoreFromDistance(distance), nil
	case metricIP:
		return vectorstore.ScoreFromOneMinusInnerProductDistance(distance), nil
	default:
		return 0, errors.New("redis: native distance metric is unsupported")
	}
}

type hashRecord struct {
	key, content, metadata, terms string
	vector                        []byte
}

// Hex confines each term to lowercase ASCII independent of TAG case handling
// and escaping. The comma separates whole tokens and cannot occur in a token.
func tagValues(m metadata.Map) (string, error) {
	terms, err := keyword.Project(m)
	if err != nil {
		return "", err
	}
	for i, t := range terms {
		terms[i] = hex.EncodeToString([]byte(t))
	}
	return strings.Join(terms, termsSeparator), nil
}
