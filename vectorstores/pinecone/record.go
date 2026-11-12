package pinecone

import (
	"errors"
	"fmt"
	"math"
	"strings"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/httpclient"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

// The server's exact metadata accounting is unpublished. Counting the SDK's
// complete protobuf Struct, including framing, with decimal KB reserves room
// within the published 40 KB limit without omitting derived metadata fields.
const metadataByteBudget = 40_000

type nativeSchema struct {
	dimensions int
	metric     pineconesdk.IndexMetric
}

// servingServerless reports the named index ready on managed serverless
// infrastructure, neither pod-based nor bring-your-own-cloud.
func servingServerless(index *pineconesdk.Index, name string) bool {
	if index == nil || index.Name != name || index.Host == "" || index.Schema == nil || index.Status == nil || !index.Status.Ready {
		return false
	}
	deployment := index.Deployment
	return deployment != nil && deployment.Managed != nil && deployment.Pod == nil && deployment.Byoc == nil
}

// validIndexHost admits the bare origin the control plane reports for an
// index; it omits the scheme, which is always HTTPS. The SDK also dials the
// host as a gRPC target, where even a root path is not part of the authority.
func validIndexHost(host string) bool {
	if !strings.Contains(host, schemeSeparator) {
		host = httpsScheme + schemeSeparator + host
	}
	_, err := httpclient.ParseOrigin(host)
	return err == nil && !strings.HasSuffix(host, "/")
}

const (
	httpsScheme     = "https"
	schemeSeparator = "://"
)

// maxIndexDimension is the largest dense vector dimension Pinecone serves.
const maxIndexDimension = 20000

func (n *nativeSchema) read(index *pineconesdk.Index, name string) error {
	if !servingServerless(index, name) {
		return fmt.Errorf("%w: requires a ready dense serverless native index with declared dimensions and host", ErrIncompatibleIndex)
	}
	vector := index.Schema.Fields[vectorField].DenseVector
	if vector == nil || vector.Dimension <= 0 || vector.Dimension > maxIndexDimension {
		return fmt.Errorf("%w: requires a dense %s field with valid dimensions", ErrIncompatibleIndex, vectorField)
	}
	if !validIndexHost(index.Host) {
		return fmt.Errorf("%w: native index has an invalid host", ErrIncompatibleIndex)
	}
	switch vector.Metric {
	case pineconesdk.IndexMetricCosine, pineconesdk.IndexMetricDotproduct, pineconesdk.IndexMetricEuclidean:
	default:
		return fmt.Errorf("%w: unsupported native metric %q", ErrIncompatibleIndex, vector.Metric)
	}
	n.dimensions = int(vector.Dimension)
	n.metric = vector.Metric
	return nil
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
		return fmt.Errorf("pinecone: vector dimension %d differs from native dimension %d", len(vector), n.dimensions)
	}
	return nil
}

// nearness orders native scores so that larger is nearer: Pinecone reports a
// similarity for cosine and dot product but a distance for Euclidean indexes.
func (n nativeSchema) nearness(raw float64) float64 {
	if n.metric == pineconesdk.IndexMetricEuclidean {
		return -raw
	}
	return raw
}

func (n nativeSchema) score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, errors.New("pinecone: native score is not finite")
	}
	switch n.metric {
	case pineconesdk.IndexMetricCosine:
		return vectorstore.ScoreFromCosineSimilarity(raw), nil
	case pineconesdk.IndexMetricDotproduct:
		return vectorstore.ScoreFromInnerProduct(raw), nil
	case pineconesdk.IndexMetricEuclidean:
		if raw < 0 {
			return 0, errors.New("pinecone: native distance is negative")
		}
		return vectorstore.ScoreFromDistance(raw), nil
	default:
		return 0, errors.New("pinecone: native metric is missing")
	}
}

func (n nativeSchema) decode(point *pineconesdk.Vector) (*document.Document, string, error) {
	if point == nil || point.Id == "" || point.Values == nil || point.SparseValues != nil || point.Metadata == nil || len(point.Metadata.Fields) != 3 {
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
	doc := new(document.Document)
	if err := doc.UnmarshalStoredMetadata([]byte(facts.StringValue)); err != nil {
		return nil, "", err
	}
	terms, err := keyword.Project(doc.Metadata)
	if err != nil {
		return nil, "", err
	}
	list := point.Metadata.Fields[termsField].GetListValue()
	if list == nil || len(list.Values) != len(terms) {
		return nil, "", errors.New("pinecone: missing or inconsistent metadata terms")
	}
	for i, v := range list.Values {
		if v.GetStringValue() != terms[i] {
			return nil, "", errors.New("pinecone: invalid derived metadata term")
		}
	}
	doc.ID, doc.Text = point.Id, text.StringValue
	if err := validateNativeIdentifier(doc.ID, false); err != nil {
		return nil, "", err
	}
	if err := vectorstore.ValidateDocument(doc); err != nil {
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
	facts, err := doc.MarshalStoredMetadata()
	if err != nil {
		return nil, err
	}
	terms, err := keyword.Project(doc.Metadata)
	if err != nil {
		return nil, err
	}
	list := &structpb.ListValue{}
	for _, t := range terms {
		list.Values = append(list.Values, structpb.NewStringValue(t))
	}
	point := &pineconesdk.Vector{Id: doc.ID, Metadata: &structpb.Struct{Fields: map[string]*structpb.Value{termsField: structpb.NewListValue(list), contentField: structpb.NewStringValue(doc.Text), metadataField: structpb.NewStringValue(string(facts))}}}
	if size := proto.Size(point.Metadata); size > metadataByteBudget {
		return nil, fmt.Errorf("%w: pinecone document %q metadata requires %d protobuf bytes; admission budget is %d", vectorstore.ErrInvalidDocument, doc.ID, size, metadataByteBudget)
	}
	return point, nil
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

// Pinecone matches membership in its indexed metadata string array. Only
// positive top-level scalar string equality is compiled; no implicit list/fetch
// is allowed for operators outside that proven subset.
func nativeFilter(predicate filter.Predicate) (*pineconesdk.MetadataFilter, error) {
	if predicate == nil {
		return nil, nil
	}
	term, err := keyword.EqualString(predicate)
	if err != nil {
		return nil, err
	}
	items := []*structpb.Value{structpb.NewStringValue(term)}
	return &structpb.Struct{Fields: map[string]*structpb.Value{termsField: structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"$in": structpb.NewListValue(&structpb.ListValue{Values: items})}})}}, nil
}

type scoredDocument struct {
	result *vectorstore.SearchResult
	rank   float64
}
