package mongodb

import (
	"errors"
	"fmt"
	"math"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type nativeSchema struct{ dimensions int }

func (n *nativeSchema) read(raw bson.Raw, name string) error {
	var index struct {
		Name       string `bson:"name"`
		Type       string `bson:"type"`
		Status     string `bson:"status"`
		Queryable  bool   `bson:"queryable"`
		Definition struct {
			Fields []struct {
				Type       string `bson:"type"`
				Path       string `bson:"path"`
				Dimensions int    `bson:"numDimensions"`
				Similarity string `bson:"similarity"`
			} `bson:"fields"`
		} `bson:"latestDefinition"`
	}
	if err := bson.Unmarshal(raw, &index); err != nil {
		return err
	}
	if index.Name != name || index.Type != "vectorSearch" || index.Status != "READY" || !index.Queryable {
		return errors.New("mongodb: requires the named READY vectorSearch index")
	}
	vectors, identity := 0, false
	for _, field := range index.Definition.Fields {
		switch field.Type {
		case "filter":
			if field.Path == idField {
				identity = true
			}
		case "vector":
			if field.Path != embeddingField || field.Dimensions <= 0 {
				return errors.New("mongodb: requires one embedding vector with a native dimension")
			}
			switch field.Similarity {
			case "cosine", "euclidean", "dotProduct":
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

func (n nativeSchema) vector(values []float64) ([]float32, error) {
	vector := embedding.Float32Vector(values)
	if err := n.validateVector(vector); err != nil {
		return nil, err
	}
	return vector, nil
}

func (n nativeSchema) validateVector(values []float32) error {
	if len(values) != n.dimensions {
		return fmt.Errorf("mongodb: vector width %d differs from native dimension %d", len(values), n.dimensions)
	}
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("mongodb: vector is not finite FLOAT32")
		}
	}
	return nil
}

func (n nativeSchema) decode(raw bson.Raw, scored bool) (*document.Document, string, error) {
	elements, err := raw.Elements()
	if err != nil {
		return nil, "", err
	}
	fields := make(map[string]bson.RawValue, len(elements))
	for _, element := range elements {
		key := element.Key()
		switch key {
		case idField, contentField, metadataField, embeddingField:
		case scoreField:
			if !scored {
				return nil, "", errors.New("mongodb: source contains a query score")
			}
		default:
			return nil, "", fmt.Errorf("mongodb: unexpected source field %q", key)
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, "", fmt.Errorf("mongodb: duplicate source field %q", key)
		}
		fields[key] = element.Value()
	}
	want := 4
	if scored {
		want++
	}
	if len(fields) != want {
		return nil, "", errors.New("mongodb: native record does not have the current shape")
	}
	id, idOK := fields[idField].StringValueOK()
	text, textOK := fields[contentField].StringValueOK()
	facts, factsOK := fields[metadataField].StringValueOK()
	array, vectorOK := fields[embeddingField].ArrayOK()
	if !idOK || !textOK || !factsOK || !vectorOK {
		return nil, "", errors.New("mongodb: identity, content, metadata_json and embedding have invalid native types")
	}
	values, err := array.Values()
	if err != nil {
		return nil, "", err
	}
	vector := make([]float32, len(values))
	for i, value := range values {
		number, ok := value.DoubleOK()
		if !ok {
			return nil, "", errors.New("mongodb: embedding must contain native doubles")
		}
		vector[i] = float32(number)
	}
	if err = n.validateVector(vector); err != nil {
		return nil, "", err
	}
	var decoded metadata.Map
	if err = decoded.UnmarshalJSON([]byte(facts)); err != nil {
		return nil, "", err
	}
	doc := &document.Document{ID: id, Text: text, Metadata: decoded}
	if err = (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, "", err
	}
	return doc, facts, nil
}

func encodeRecord(doc *document.Document) (bson.M, error) {
	if doc.Media != nil {
		return nil, vectorstore.ErrInvalidDocument
	}
	facts, err := doc.Metadata.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return bson.M{idField: doc.ID, contentField: doc.Text, metadataField: string(facts)}, nil
}

type selectedDocument struct{ id, metadataJSON string }
