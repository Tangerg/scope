package weaviate

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/weaviate/weaviate-go-client/v6/collections"
	"github.com/weaviate/weaviate-go-client/v6/collections/vectorindex"
	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

type nativeSchema struct{ metric string }

type indexWire struct {
	Distance string `json:"distance"`
	Skip     bool   `json:"skip"`
	Multi    struct {
		Enabled bool `json:"enabled"`
	} `json:"multivector"`
}
type propertyWire struct {
	Name            string   `json:"name"`
	DataType        []string `json:"dataType"`
	Tokenization    string   `json:"tokenization"`
	IndexSearchable *bool    `json:"indexSearchable"`
	IndexFilterable *bool    `json:"indexFilterable"`
}
type vectorWire struct {
	Vectorizer map[string]json.RawMessage `json:"vectorizer"`
	IndexType  string                     `json:"vectorIndexType"`
	Index      struct {
		indexWire
		HNSW indexWire `json:"hnsw"`
		Flat indexWire `json:"flat"`
	} `json:"vectorIndexConfig"`
}
type collectionWire struct {
	Name         string `json:"class"`
	Description  string `json:"description"`
	MultiTenancy struct {
		Enabled bool `json:"enabled"`
	} `json:"multiTenancyConfig"`
	Vectors    map[string]vectorWire `json:"vectorConfig"`
	Properties []propertyWire        `json:"properties"`
}

func (n *nativeSchema) read(c *collectionWire, name string, space vectorstore.SpaceID) error {
	if c == nil || c.Name != name || len(c.Vectors) != 1 || c.MultiTenancy.Enabled {
		return fmt.Errorf("%w: wrong name, tenant or vector topology", ErrIncompatibleCollection)
	}
	if c.Description != space.StorageKey() {
		return vectorstore.ErrIncompatibleSpace
	}
	v, found := c.Vectors[vectorName]
	none, explicit := v.Vectorizer["none"]
	if !found || len(v.Vectorizer) != 1 || !explicit || len(none) == 0 || none[0] != '{' {
		return fmt.Errorf("%w: expected a self-provided dense vector named %q", ErrIncompatibleCollection, vectorName)
	}
	idx := v.Index
	if idx.Skip || idx.Multi.Enabled {
		return fmt.Errorf("%w: disabled or multivector index", ErrIncompatibleCollection)
	}
	switch vectorindex.Type(v.IndexType) {
	case (vectorindex.Flat{}).Name(), (vectorindex.HNSW{}).Name():
	case (vectorindex.Dynamic{}).Name():
		for _, sub := range []indexWire{idx.HNSW, idx.Flat} {
			if sub.Skip || sub.Multi.Enabled || sub.Distance != "" && sub.Distance != idx.Distance {
				return fmt.Errorf("%w: inconsistent dynamic index", ErrIncompatibleCollection)
			}
		}
	default:
		return fmt.Errorf("%w: unsupported index %q", ErrIncompatibleCollection, v.IndexType)
	}
	switch idx.Distance {
	case distanceCosine, distanceDot, distanceL2Squared, distanceHamming, distanceManhattan:
	default:
		return fmt.Errorf("%w: invalid distance %q", ErrIncompatibleCollection, idx.Distance)
	}
	if len(c.Properties) != 3 {
		return fmt.Errorf("%w: expected three document properties", ErrIncompatibleCollection)
	}
	seen := make(map[string]bool, 3)
	for _, p := range c.Properties {
		if seen[p.Name] || len(p.DataType) != 1 {
			return fmt.Errorf("%w: invalid property %q", ErrIncompatibleCollection, p.Name)
		}
		seen[p.Name] = true
		switch p.Name {
		case fieldContent:
			if p.DataType[0] != string(collections.DataTypeText) || p.Tokenization != string(collections.TokenizationWord) || p.IndexSearchable != nil && !*p.IndexSearchable {
				return fmt.Errorf("%w: content must be searchable word text", ErrIncompatibleCollection)
			}
		case fieldMetadata, fieldTerms:
			want := string(collections.DataTypeText)
			if p.Name == fieldTerms {
				want = string(collections.DataTypeTextArray)
			}
			if p.DataType[0] != want || p.Tokenization != string(collections.TokenizationField) || p.IndexFilterable != nil && !*p.IndexFilterable {
				return fmt.Errorf("%w: property %q must be filterable field %s", ErrIncompatibleCollection, p.Name, want)
			}
		default:
			return fmt.Errorf("%w: unexpected property %q", ErrIncompatibleCollection, p.Name)
		}
	}
	n.metric = idx.Distance
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
		if raw < 0 || raw > 2 {
			return 0, errors.New("weaviate: invalid native cosine distance")
		}
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
	vector := corevector.Float32(values)
	if err := validateVector(vector, dimensions); err != nil {
		return nil, err
	}
	return vector, nil
}

func validateVector(vector []float32, dimensions int) error {
	if err := corevector.Coordinates[float32](vector).Validate(); err != nil {
		return err
	}
	if len(vector) == 0 || dimensions != 0 && len(vector) != dimensions {
		return fmt.Errorf("weaviate: vector width %d differs from observed native dimension %d", len(vector), dimensions)
	}
	return nil
}

func decodeDocument(id, text, facts string, reported []string) (*document.Document, error) {
	if err := validateObjectID(id); err != nil {
		return nil, err
	}
	doc := new(document.Document)
	if err := doc.UnmarshalStoredMetadata([]byte(facts)); err != nil {
		return nil, err
	}
	expected, err := nativeTerms(doc.Metadata)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(expected, reported) {
		return nil, errors.New("weaviate: terms differ from canonical metadata")
	}
	doc.ID, doc.Text = id, text
	if err := vectorstore.ValidateDocument(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func encodeRecord(doc *document.Document) (*pb.BatchObject, error) {
	if err := validateObjectID(doc.ID); err != nil {
		return nil, err
	}
	facts, err := doc.MarshalStoredMetadata()
	if err != nil {
		return nil, err
	}
	terms, err := nativeTerms(doc.Metadata)
	if err != nil {
		return nil, err
	}
	return &pb.BatchObject{Uuid: doc.ID, Properties: &pb.BatchObject_Properties{
		NonRefProperties:    &structpb.Struct{Fields: map[string]*structpb.Value{fieldContent: structpb.NewStringValue(doc.Text), fieldMetadata: structpb.NewStringValue(string(facts))}},
		TextArrayProperties: []*pb.TextArrayProperties{{PropName: fieldTerms, Values: terms}},
	}}, nil
}

type rankedDocument struct {
	result *vectorstore.SearchResult
	rank   float64
}

func nativeTerms(facts metadata.Map) ([]string, error) {
	terms, err := keyword.Project(facts)
	if err != nil {
		return nil, err
	}
	for i, t := range terms {
		terms[i] = hex.EncodeToString([]byte(t))
	}
	return terms, nil
}
