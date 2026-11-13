package qdrant

import (
	"errors"
	"fmt"
	"math"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

type nativeSchema struct {
	dimensions int
	metric     qdrantclient.Distance
}

func (n *nativeSchema) read(info *qdrantclient.CollectionInfo) error {
	params := info.GetConfig().GetParams()
	vector := params.GetVectorsConfig().GetParams()
	if vector == nil || vector.Size == 0 || vector.Size > math.MaxInt || vector.MultivectorConfig != nil || len(params.GetSparseVectorsConfig().GetMap()) != 0 || params.GetShardingMethod() == qdrantclient.ShardingMethod_Custom {
		return fmt.Errorf("%w: requires one unnamed dense vector with automatic sharding", ErrIncompatibleCollection)
	}
	if kind := vector.GetDatatype(); kind != qdrantclient.Datatype_Default && kind != qdrantclient.Datatype_Float32 {
		return fmt.Errorf("%w: requires native FLOAT32 storage", ErrIncompatibleCollection)
	}
	switch vector.Distance {
	case qdrantclient.Distance_Cosine, qdrantclient.Distance_Dot, qdrantclient.Distance_Euclid, qdrantclient.Distance_Manhattan:
	default:
		return fmt.Errorf("%w: unsupported native metric", ErrIncompatibleCollection)
	}
	n.dimensions, n.metric = int(vector.Size), vector.Distance
	return nil
}

func (n nativeSchema) vector(values []float64) ([]float32, error) {
	if err := vectorstore.RequireWidth(values, n.dimensions); err != nil {
		return nil, err
	}
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
		return fmt.Errorf("qdrant: vector width %d differs from native dimension %d", len(vector), n.dimensions)
	}
	return nil
}

func (n nativeSchema) score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, errors.New("qdrant: native score is not finite")
	}
	switch n.metric {
	case qdrantclient.Distance_Cosine:
		return vectorstore.ScoreFromCosineSimilarity(raw), nil
	case qdrantclient.Distance_Dot:
		return vectorstore.ScoreFromInnerProduct(raw), nil
	case qdrantclient.Distance_Euclid, qdrantclient.Distance_Manhattan:
		if raw < 0 {
			return 0, errors.New("qdrant: native distance is negative")
		}
		return vectorstore.ScoreFromDistance(raw), nil
	default:
		return 0, errors.New("qdrant: native metric is missing")
	}
}

// nearness orders native scores so that larger is nearer: Qdrant reports a
// similarity for cosine and dot product but a distance for the other metrics.
func (n nativeSchema) nearness(raw float64) float64 {
	if n.metric == qdrantclient.Distance_Euclid || n.metric == qdrantclient.Distance_Manhattan {
		return -raw
	}
	return raw
}

func (n nativeSchema) decode(id *qdrantclient.PointId, payload map[string]*qdrantclient.Value, vectors *qdrantclient.VectorsOutput) (*document.Document, string, error) {
	identity, err := formatPointID(id)
	if err != nil {
		return nil, "", err
	}
	if vectors.GetVector().GetDense() == nil {
		return nil, "", errors.New("qdrant: native record lacks the current dense vector")
	}
	if err = n.validateVector(vectors.GetVector().GetDense().Data); err != nil {
		return nil, "", err
	}
	if len(payload) != 3 || payload[contentField] == nil || payload[metadataField] == nil {
		return nil, "", errors.New("qdrant: native record does not have the current payload shape")
	}
	content, contentOK := payload[contentField].Kind.(*qdrantclient.Value_StringValue)
	facts, factsOK := payload[metadataField].Kind.(*qdrantclient.Value_StringValue)
	if !contentOK || !factsOK {
		return nil, "", errors.New("qdrant: content and metadata_json must be strings")
	}
	doc := new(document.Document)
	if err = doc.UnmarshalStoredMetadata([]byte(facts.StringValue)); err != nil {
		return nil, "", err
	}
	terms, err := keyword.Project(doc.Metadata)
	if err != nil {
		return nil, "", err
	}
	if payload[termsField] == nil || payload[termsField].GetListValue() == nil {
		return nil, "", errors.New("qdrant: missing derived metadata terms")
	}
	if got := payload[termsField].GetListValue().GetValues(); len(got) != len(terms) {
		return nil, "", errors.New("qdrant: inconsistent derived metadata terms")
	} else {
		for i, v := range got {
			if v.GetStringValue() != terms[i] {
				return nil, "", errors.New("qdrant: corrupted derived metadata term")
			}
		}
	}
	doc.ID, doc.Text = identity, content.StringValue
	if err = vectorstore.ValidateDocument(doc); err != nil {
		return nil, "", err
	}
	return doc, facts.StringValue, nil
}

func encodeRecord(doc *document.Document) (*qdrantclient.PointStruct, error) {
	if doc.Media != nil {
		return nil, vectorstore.ErrInvalidDocument
	}
	id, err := parsePointID(doc.ID)
	if err != nil {
		return nil, err
	}
	facts, err := doc.MarshalStoredMetadata()
	if err != nil {
		return nil, err
	}
	terms, err := keyword.Project(doc.Metadata)
	if err != nil {
		return nil, err
	}
	list := &qdrantclient.ListValue{}
	for _, t := range terms {
		list.Values = append(list.Values, qdrantclient.NewValueString(t))
	}
	return &qdrantclient.PointStruct{Id: id, Payload: map[string]*qdrantclient.Value{
		termsField:    {Kind: &qdrantclient.Value_ListValue{ListValue: list}},
		contentField:  {Kind: &qdrantclient.Value_StringValue{StringValue: doc.Text}},
		metadataField: {Kind: &qdrantclient.Value_StringValue{StringValue: string(facts)}},
	}}, nil
}

type scoredDocument struct {
	result *vectorstore.SearchResult
	rank   float64
}
