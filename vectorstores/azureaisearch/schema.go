package azureaisearch

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Tangerg/scope/core/vectorstore"
)

var ErrIncompatibleIndex = errors.New("azureaisearch: index is incompatible")

type nativeMetric string

const (
	metricCosine    nativeMetric = "cosine"
	metricDot       nativeMetric = "dotProduct"
	metricEuclidean nativeMetric = "euclidean"
)

func (n nativeMetric) valid() bool {
	return n == metricCosine || n == metricDot || n == metricEuclidean
}

func (n nativeMetric) score(raw float64, mode vectorstore.SearchMode) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return 0, errors.New("azureaisearch: invalid native score")
	}
	if mode == vectorstore.SearchModeSemantic && n == metricCosine {
		if raw < float64(math.Nextafter32(1.0/3, 0)) || raw > 1 {
			return 0, errors.New("azureaisearch: cosine score is outside the native range")
		}
		return vectorstore.ScoreFromCosineSimilarity(2 - 1/raw), nil
	}
	// Azure publishes an invertible transform only for cosine. Other native
	// scores, including hybrid RRF, retain their native ranking and clamp to Core.
	return vectorstore.ScoreFromValue(raw), nil
}

type indexField struct {
	Name                string `json:"name"`
	Type                string `json:"type"`
	Key                 bool   `json:"key"`
	Filterable          bool   `json:"filterable"`
	Sortable            bool   `json:"sortable"`
	Searchable          bool   `json:"searchable"`
	Facetable           bool   `json:"facetable"`
	Retrievable         *bool  `json:"retrievable"`
	Stored              *bool  `json:"stored"`
	Normalizer          string `json:"normalizer"`
	Dimensions          int    `json:"dimensions"`
	VectorSearchProfile string `json:"vectorSearchProfile"`
}

type indexSchema struct {
	Fields       []indexField `json:"fields"`
	VectorSearch struct {
		Profiles []struct {
			Name      string `json:"name"`
			Algorithm string `json:"algorithm"`
		} `json:"profiles"`
		Algorithms []indexAlgorithm `json:"algorithms"`
	} `json:"vectorSearch"`
}

func (i indexSchema) bind(idField, contentField, embeddingField, metadataField string) (nativeMetric, int, error) {
	if len(i.Fields) != 4 {
		return "", 0, fmt.Errorf("%w: exactly four native storage fields are required", ErrIncompatibleIndex)
	}
	fields := make(map[string]indexField, 4)
	for _, field := range i.Fields {
		if _, duplicate := fields[field.Name]; duplicate {
			return "", 0, fmt.Errorf("%w: duplicate field %q", ErrIncompatibleIndex, field.Name)
		}
		if field.Retrievable != nil && !*field.Retrievable {
			return "", 0, fmt.Errorf("%w: field %q is not retrievable", ErrIncompatibleIndex, field.Name)
		}
		fields[field.Name] = field
	}
	id, exists := fields[idField]
	if !exists || id.Type != "Edm.String" || !id.Key || !id.Filterable || !id.Sortable || id.Normalizer != "" {
		return "", 0, fmt.Errorf("%w: ID must be a retrievable, filterable, sortable string key without a normalizer", ErrIncompatibleIndex)
	}
	content, exists := fields[contentField]
	if !exists || content.Type != "Edm.String" || content.Key || !content.Searchable {
		return "", 0, fmt.Errorf("%w: content must be a searchable string", ErrIncompatibleIndex)
	}
	facts, exists := fields[metadataField]
	if !exists || facts.Type != "Edm.String" || facts.Key || facts.Searchable || facts.Filterable || facts.Sortable || facts.Facetable {
		return "", 0, fmt.Errorf("%w: metadata must be an unindexed string containing Core JSON", ErrIncompatibleIndex)
	}
	vector, exists := fields[embeddingField]
	if !exists || vector.Type != "Collection(Edm.Single)" || vector.Key || !vector.Searchable || vector.Dimensions <= 0 || vector.VectorSearchProfile == "" || (vector.Stored != nil && !*vector.Stored) {
		return "", 0, fmt.Errorf("%w: vector must have a searchable, stored single-precision profile and positive dimensions", ErrIncompatibleIndex)
	}
	algorithmName := ""
	foundProfile := false
	for _, profile := range i.VectorSearch.Profiles {
		if profile.Name != vector.VectorSearchProfile {
			continue
		}
		if foundProfile {
			return "", 0, fmt.Errorf("%w: duplicate vector profile", ErrIncompatibleIndex)
		}
		foundProfile = true
		algorithmName = profile.Algorithm
	}
	if algorithmName == "" {
		return "", 0, fmt.Errorf("%w: native vector profile resolves to no algorithm", ErrIncompatibleIndex)
	}
	var metric nativeMetric
	foundAlgorithm := false
	for _, algorithm := range i.VectorSearch.Algorithms {
		if algorithm.Name != algorithmName {
			continue
		}
		if foundAlgorithm {
			return "", 0, fmt.Errorf("%w: duplicate native vector algorithm", ErrIncompatibleIndex)
		}
		foundAlgorithm = true
		var err error
		metric, err = algorithm.metric()
		if err != nil {
			return "", 0, err
		}
	}
	if !foundAlgorithm {
		return "", 0, fmt.Errorf("%w: native vector algorithm is absent", ErrIncompatibleIndex)
	}
	return metric, vector.Dimensions, nil
}

func validFieldName(name string) bool {
	if len(name) == 0 || len(name) > 128 || strings.HasPrefix(name, "azureSearch") {
		return false
	}
	for i, character := range []byte(name) {
		letter := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
		if !letter && (i == 0 || (character != '_' && (character < '0' || character > '9'))) {
			return false
		}
	}
	return true
}

func validateID(id string) error {
	if len(id) == 0 || len(id) > 1024 || id[0] == '_' {
		return fmt.Errorf("azureaisearch: %w: unsupported native document key %q", vectorstore.ErrInvalidDocument, id)
	}
	for _, character := range []byte(id) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '=' {
			continue
		}
		return fmt.Errorf("azureaisearch: %w: unsupported native document key %q", vectorstore.ErrInvalidDocument, id)
	}
	return nil
}
