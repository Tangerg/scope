package pinecone

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"

	pineconesdk "github.com/pinecone-io/go-pinecone/v4/pinecone"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type nativeSchema struct {
	dimensions int
	metric     pineconesdk.IndexMetric
}

func (n *nativeSchema) read(index *pineconesdk.Index, name string) error {
	if index == nil || index.Name != name || index.Host == "" || index.Dimension == nil || *index.Dimension <= 0 || *index.Dimension > 20000 || index.VectorType != "dense" || index.Status == nil || !index.Status.Ready || index.Spec == nil || index.Spec.Serverless == nil || index.Spec.Pod != nil {
		return fmt.Errorf("%w: requires a ready dense serverless native index with declared dimensions and host", ErrIncompatibleIndex)
	}
	host := index.Host
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	parsed, err := url.Parse(host)
	if err != nil || parsed.Hostname() == "" || strings.TrimSpace(host) != host || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: native index has an invalid host", ErrIncompatibleIndex)
	}
	switch index.Metric {
	case pineconesdk.Cosine, pineconesdk.Dotproduct, pineconesdk.Euclidean:
	default:
		return fmt.Errorf("%w: unsupported native metric %q", ErrIncompatibleIndex, index.Metric)
	}
	n.dimensions = int(*index.Dimension)
	n.metric = index.Metric
	return nil
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
		return fmt.Errorf("pinecone: vector dimension %d differs from native dimension %d", len(vector), n.dimensions)
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("pinecone: vector is not finite FLOAT32")
		}
	}
	return nil
}

func (n nativeSchema) score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, errors.New("pinecone: native score is not finite")
	}
	switch n.metric {
	case pineconesdk.Cosine:
		return vectorstore.ScoreFromCosineSimilarity(raw), nil
	case pineconesdk.Dotproduct:
		return vectorstore.ScoreFromInnerProduct(raw), nil
	case pineconesdk.Euclidean:
		if raw < 0 {
			return 0, errors.New("pinecone: native distance is negative")
		}
		return vectorstore.ScoreFromDistance(raw), nil
	default:
		return 0, errors.New("pinecone: native metric is missing")
	}
}

func (n nativeSchema) decode(point *pineconesdk.Vector) (*document.Document, string, error) {
	if point == nil || point.Id == "" || point.Values == nil || point.SparseValues != nil || point.Metadata == nil || len(point.Metadata.Fields) != 2 {
		return nil, "", errors.New("pinecone: native record does not have the current dense shape")
	}
	if err := n.validateVector(*point.Values); err != nil {
		return nil, "", err
	}
	content := point.Metadata.Fields[contentField]
	payload := point.Metadata.Fields[metadataField]
	if content == nil || payload == nil {
		return nil, "", errors.New("pinecone: native record is missing content or metadata_json")
	}
	text, textOK := content.Kind.(*structpb.Value_StringValue)
	facts, factsOK := payload.Kind.(*structpb.Value_StringValue)
	if !textOK || !factsOK {
		return nil, "", errors.New("pinecone: native content and metadata_json must be strings")
	}
	var values metadata.Map
	if err := values.UnmarshalJSON([]byte(facts.StringValue)); err != nil {
		return nil, "", err
	}
	doc := &document.Document{ID: point.Id, Text: text.StringValue, Metadata: values}
	if err := validateNativeIdentifier(doc.ID, false); err != nil {
		return nil, "", err
	}
	if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, "", err
	}
	return doc, facts.StringValue, nil
}

func encodeRecord(doc *document.Document) (*pineconesdk.Vector, error) {
	if err := validateNativeIdentifier(doc.ID, false); err != nil {
		return nil, err
	}
	if doc.Media != nil {
		return nil, vectorstore.ErrInvalidDocument
	}
	facts, err := doc.Metadata.MarshalJSON()
	if err != nil {
		return nil, err
	}
	// Protobuf stores two strings; Core alone encodes the metadata value domain.
	return &pineconesdk.Vector{Id: doc.ID, Metadata: &structpb.Struct{Fields: map[string]*structpb.Value{contentField: structpb.NewStringValue(doc.Text), metadataField: structpb.NewStringValue(string(facts))}}}, nil
}

func validateNativeIdentifier(value string, allowEmpty bool) error {
	if value == "" && !allowEmpty {
		return vectorstore.ErrMissingDocumentID
	}
	if len(value) > 512 {
		return errors.New("pinecone: native identifier exceeds 512 ASCII characters")
	}
	for i := range len(value) {
		if value[i] == 0 || value[i] > 127 {
			return errors.New("pinecone: native identifier must be ASCII without NUL")
		}
	}
	return nil
}

func metadataSelection(values []string) *pineconesdk.MetadataFilter {
	items := make([]*structpb.Value, len(values))
	for i, value := range values {
		items[i] = structpb.NewStringValue(value)
	}
	return &structpb.Struct{Fields: map[string]*structpb.Value{metadataField: structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"$in": structpb.NewListValue(&structpb.ListValue{Values: items})}})}}
}

type scoredDocument struct {
	result *vectorstore.SearchResult
	rank   float64
}
