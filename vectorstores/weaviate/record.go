package weaviate

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/go-openapi/strfmt"
	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type nativeSchema struct{ metric string }

func (n *nativeSchema) read(class *models.Class, name string) error {
	if class == nil || class.Class != name || class.Vectorizer != "none" || len(class.VectorConfig) != 0 || class.MultiTenancyConfig != nil && class.MultiTenancyConfig.Enabled {
		return ErrIncompatibleClass
	}
	config, ok := class.VectorIndexConfig.(map[string]any)
	if !ok {
		return ErrIncompatibleClass
	}
	metric, ok := config["distance"].(string)
	if !ok {
		return ErrIncompatibleClass
	}
	switch metric {
	case distanceCosine, distanceDot, distanceL2Squared, distanceHamming, distanceManhattan:
	default:
		return ErrIncompatibleClass
	}
	if skip, ok := config["skip"].(bool); ok && skip {
		return ErrIncompatibleClass
	}
	if multi, ok := config["multivector"].(map[string]any); ok && multi["enabled"] == true {
		return ErrIncompatibleClass
	}
	switch class.VectorIndexType {
	case "hnsw", "flat", "dynamic":
	default:
		return ErrIncompatibleClass
	}
	if len(class.Properties) != 2 {
		return ErrIncompatibleClass
	}
	seen := make(map[string]struct{})
	for _, property := range class.Properties {
		if property == nil || len(property.DataType) != 1 || property.DataType[0] != "text" {
			return ErrIncompatibleClass
		}
		if _, duplicate := seen[property.Name]; duplicate {
			return ErrIncompatibleClass
		}
		seen[property.Name] = struct{}{}
		switch property.Name {
		case fieldContent:
			if property.Tokenization != "word" || property.IndexSearchable != nil && !*property.IndexSearchable {
				return ErrIncompatibleClass
			}
		case fieldMetadata:
			if property.Tokenization != "field" || property.IndexFilterable != nil && !*property.IndexFilterable {
				return ErrIncompatibleClass
			}
		default:
			return ErrIncompatibleClass
		}
	}
	n.metric = metric
	return nil
}

func (n nativeSchema) score(raw float64, mode vectorstore.SearchMode) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, errors.New("weaviate: native relevance is not finite")
	}
	if mode == vectorstore.SearchModeHybrid {
		score := vectorstore.Score(raw)
		return score, score.Validate()
	}
	switch n.metric {
	case distanceCosine:
		return vectorstore.ScoreFromCosineDistance(raw), nil
	case distanceDot:
		return vectorstore.ScoreFromNegativeInnerProductDistance(raw), nil
	case distanceL2Squared, distanceHamming, distanceManhattan:
		if raw < 0 {
			return 0, errors.New("weaviate: native distance is negative")
		}
		return vectorstore.ScoreFromDistance(raw), nil
	default:
		return 0, errors.New("weaviate: native metric is missing")
	}
}

func nativeVector(values []float64, dimensions int) ([]float32, error) {
	vector := embedding.Float32Vector(values)
	if err := validateVector(vector, dimensions); err != nil {
		return nil, err
	}
	return vector, nil
}

func validateVector(vector []float32, dimensions int) error {
	if len(vector) == 0 || dimensions != 0 && len(vector) != dimensions {
		return fmt.Errorf("weaviate: vector width %d differs from observed native dimension %d", len(vector), dimensions)
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("weaviate: vector is not finite FLOAT32")
		}
	}
	return nil
}

func decodeDocument(id string, properties map[string]any) (*document.Document, string, error) {
	if err := validateObjectID(id); err != nil {
		return nil, "", err
	}
	if len(properties) != 2 {
		return nil, "", errors.New("weaviate: native record does not have the current property shape")
	}
	text, textOK := properties[fieldContent].(string)
	facts, factsOK := properties[fieldMetadata].(string)
	if !textOK || !factsOK {
		return nil, "", errors.New("weaviate: content and metadata must be strings")
	}
	var values metadata.Map
	if err := values.UnmarshalJSON([]byte(facts)); err != nil {
		return nil, "", err
	}
	doc := &document.Document{ID: id, Text: text, Metadata: values}
	if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, "", err
	}
	return doc, facts, nil
}

func encodeRecord(doc *document.Document, name string) (*models.Object, error) {
	if err := validateObjectID(doc.ID); err != nil {
		return nil, err
	}
	facts, err := doc.Metadata.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return &models.Object{ID: strfmt.UUID(doc.ID), Class: name, Properties: map[string]any{fieldContent: doc.Text, fieldMetadata: string(facts)}}, nil
}

func graphQLVector(value any, dimensions int) error {
	values, ok := value.([]any)
	if !ok {
		return errors.New("weaviate: query lacks the native dense vector")
	}
	vector := make([]float32, len(values))
	for i, item := range values {
		number, ok := item.(float64)
		if !ok {
			return errors.New("weaviate: query vector contains a non-number")
		}
		vector[i] = float32(number)
	}
	return validateVector(vector, dimensions)
}

func graphQLRelevance(additional map[string]any, mode vectorstore.SearchMode) (float64, error) {
	if mode == vectorstore.SearchModeSemantic {
		value, ok := additional[additionalDistance].(float64)
		if !ok {
			return 0, errors.New("weaviate: native distance is not a number")
		}
		return value, nil
	}
	text, ok := additional[additionalScore].(string)
	if !ok {
		return 0, errors.New("weaviate: native hybrid score is not a string")
	}
	return strconv.ParseFloat(text, 64)
}

type selectedDocument struct{ id, metadataJSON string }
type rankedDocument struct {
	result *vectorstore.SearchResult
	rank   float64
}
