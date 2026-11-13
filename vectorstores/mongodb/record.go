package mongodb

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
)

type nativeSimilarity string

const (
	simCosine     nativeSimilarity = "cosine"
	simEuclidean  nativeSimilarity = "euclidean"
	simDotProduct nativeSimilarity = "dotProduct"
)

// Unit vectors can lose exact unit length when their coordinates become FLOAT32.
// One FLOAT32 ULP at one admits that rounding without normalizing host input.
const unitMagnitudeTolerance = 1.0 / (1 << 23)

type nativeSchema struct {
	dimensions   int
	similarity   nativeSimilarity
	vectorPath   string
	indexedTerms bool
}

func (n *nativeSchema) read(raw bson.Raw, name string, space vectorstore.SpaceID) error {
	const (
		indexTypeVectorSearch = "vectorSearch"
		indexStatusReady      = "READY"
		indexFieldFilter      = "filter"
		indexFieldVector      = "vector"
	)
	var index struct {
		Name       string `bson:"name"`
		Type       string `bson:"type"`
		Status     string `bson:"status"`
		Queryable  bool   `bson:"queryable"`
		Definition struct {
			Fields []struct {
				Type       string           `bson:"type"`
				Path       string           `bson:"path"`
				Dimensions int              `bson:"numDimensions"`
				Similarity nativeSimilarity `bson:"similarity"`
			} `bson:"fields"`
		} `bson:"latestDefinition"`
	}
	if err := bson.Unmarshal(raw, &index); err != nil {
		return err
	}
	if index.Name != name || index.Type != indexTypeVectorSearch || index.Status != indexStatusReady || !index.Queryable {
		return errors.New("mongodb: requires the named READY vectorSearch index")
	}
	vectors, identity := 0, false
	for _, field := range index.Definition.Fields {
		switch field.Type {
		case indexFieldFilter:
			if field.Path == termsField {
				n.indexedTerms = true
			}
			if field.Path == idField {
				identity = true
			}
		case indexFieldVector:
			if field.Path != space.StorageKey() {
				return vectorstore.ErrIncompatibleSpace
			}
			n.vectorPath = field.Path
			n.similarity = field.Similarity
			if field.Dimensions <= 0 {
				return errors.New("mongodb: requires one embedding vector with a native dimension")
			}
			switch field.Similarity {
			case simCosine, simEuclidean, simDotProduct:
			default:
				return errors.New("mongodb: unsupported native vector similarity")
			}
			vectors++
			n.dimensions = field.Dimensions
		default:
			return errors.New("mongodb: unsupported native vector index field")
		}
	}
	if vectors != 1 || !identity {
		return errors.New("mongodb: requires one embedding vector and an _id filter path")
	}
	return nil
}

// scoped restricts a native filter to this store's namespace: a document
// belongs to it only when it carries the store's vector path.
func (n nativeSchema) scoped(where bson.M) bson.M {
	where[n.vectorPath] = bson.M{"$exists": true}
	return where
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

func (n nativeSchema) validateVector(values []float32) error {
	if err := corevector.Coordinates[float32](values).Validate(); err != nil {
		return err
	}
	if len(values) != n.dimensions {
		return fmt.Errorf("mongodb: vector width %d differs from native dimension %d", len(values), n.dimensions)
	}
	var squaredMagnitude float64
	for _, value := range values {
		coordinate := float64(value)
		squaredMagnitude += coordinate * coordinate
	}
	if n.similarity == simCosine && squaredMagnitude == 0 {
		return errors.New("mongodb: cosine requires a nonzero FLOAT32 vector")
	}
	if n.similarity == simDotProduct && math.Abs(math.Sqrt(squaredMagnitude)-1) > unitMagnitudeTolerance {
		return errors.New("mongodb: dotProduct requires a unit FLOAT32 vector")
	}
	return nil
}

func (n nativeSchema) decode(raw bson.Raw, scored bool) (*document.Document, string, error) {
	fields, err := n.sourceFields(raw, scored)
	if err != nil {
		return nil, "", err
	}
	id, idOK := fields[idField].StringValueOK()
	text, textOK := fields[contentField].StringValueOK()
	facts, factsOK := fields[metadataField].StringValueOK()
	array, vectorOK := fields[n.vectorPath].ArrayOK()
	if !idOK || !textOK || !factsOK || !vectorOK {
		return nil, "", errors.New("mongodb: identity, content, metadata_json and embedding have invalid native types")
	}
	if err = n.validateStoredVector(array); err != nil {
		return nil, "", err
	}
	doc := new(document.Document)
	if err = doc.UnmarshalStoredMetadata([]byte(facts)); err != nil {
		return nil, "", err
	}
	expected, err := keyword.Project(doc.Metadata)
	if err != nil {
		return nil, "", err
	}
	var reported []string
	if decodeErr := fields[termsField].Unmarshal(&reported); decodeErr != nil || !slices.Equal(reported, expected) {
		return nil, "", errors.New("mongodb: malformed derived metadata terms")
	}
	doc.ID, doc.Text = id, text
	if err = vectorstore.ValidateDocument(doc); err != nil {
		return nil, "", err
	}
	return doc, facts, nil
}

// sourceFields requires exactly the fields this store writes, plus the search
// score when the source answers a query.
func (n nativeSchema) sourceFields(raw bson.Raw, scored bool) (map[string]bson.RawValue, error) {
	elements, err := raw.Elements()
	if err != nil {
		return nil, err
	}
	stored := []string{idField, contentField, metadataField, n.vectorPath, termsField}
	want := len(stored)
	if scored {
		want++
	}
	fields := make(map[string]bson.RawValue, len(elements))
	for _, element := range elements {
		key := element.Key()
		if !slices.Contains(stored, key) && (key != scoreField || !scored) {
			return nil, fmt.Errorf("mongodb: unexpected source field %q", key)
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("mongodb: duplicate source field %q", key)
		}
		fields[key] = element.Value()
	}
	if len(fields) != want {
		return nil, errors.New("mongodb: native record does not have the current shape")
	}
	return fields, nil
}

func (n nativeSchema) validateStoredVector(array bson.RawArray) error {
	values, err := array.Values()
	if err != nil {
		return err
	}
	vector := make([]float32, len(values))
	for i, value := range values {
		number, ok := value.DoubleOK()
		if !ok {
			return errors.New("mongodb: embedding must contain native doubles")
		}
		vector[i] = float32(number)
	}
	return n.validateVector(vector)
}

func encodeRecord(doc *document.Document) (bson.M, error) {
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
	return bson.M{idField: doc.ID, contentField: doc.Text, metadataField: string(facts), termsField: terms}, nil
}

type selectedDocument struct{ id, metadataJSON string }
