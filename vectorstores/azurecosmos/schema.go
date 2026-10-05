package azurecosmos

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type containerSchema struct {
	dimensions int
	function   azcosmos.VectorDistanceFunction
}

func newContainerSchema(properties *azcosmos.ContainerProperties, name, partition string) (containerSchema, error) {
	if properties == nil || properties.ID != name || properties.PartitionKeyDefinition.Kind != azcosmos.PartitionKeyKindHash || len(properties.PartitionKeyDefinition.Paths) != 1 || properties.PartitionKeyDefinition.Paths[0] != "/partition_key" || properties.VectorEmbeddingPolicy == nil {
		return containerSchema{}, fmt.Errorf("%w: native identity, Hash partition path /partition_key or vector policy is invalid", ErrIncompatibleContainer)
	}
	limit := 101
	switch properties.PartitionKeyDefinition.Version {
	case 0, 1:
	case 2:
		limit = 2048
	default:
		return containerSchema{}, fmt.Errorf("%w: unsupported native Hash partition version", ErrIncompatibleContainer)
	}
	if len(partition) > limit {
		return containerSchema{}, fmt.Errorf("azurecosmos: partition key exceeds the native %d-byte limit", limit)
	}
	var found *azcosmos.VectorEmbedding
	for _, policy := range properties.VectorEmbeddingPolicy.VectorEmbeddings {
		if policy.Path != "/embedding" {
			continue
		}
		if found != nil {
			return containerSchema{}, fmt.Errorf("%w: duplicate /embedding policies", ErrIncompatibleContainer)
		}
		found = &policy
	}
	if found == nil || found.DataType != azcosmos.VectorDataTypeFloat32 || found.Dimensions < 1 || found.Dimensions > 4096 {
		return containerSchema{}, fmt.Errorf("%w: /embedding must declare float32 dimensions in [1,4096]", ErrIncompatibleContainer)
	}
	switch found.DistanceFunction {
	case azcosmos.VectorDistanceFunctionCosine, azcosmos.VectorDistanceFunctionDotProduct, azcosmos.VectorDistanceFunctionEuclidean:
		return containerSchema{dimensions: int(found.Dimensions), function: found.DistanceFunction}, nil
	default:
		return containerSchema{}, fmt.Errorf("%w: native distance function %q is unsupported", ErrIncompatibleContainer, found.DistanceFunction)
	}
}

func (c containerSchema) narrow(vector []float64) ([]float32, error) {
	narrowed := embedding.Float32Vector(vector)
	if err := c.validateVector(narrowed); err != nil {
		return nil, err
	}
	return narrowed, nil
}

func (c containerSchema) validateVector(vector []float32) error {
	if len(vector) != c.dimensions {
		return fmt.Errorf("azurecosmos: vector dimension %d differs from native dimension %d", len(vector), c.dimensions)
	}
	nonzero := false
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("azurecosmos: vector is not finite float32")
		}
		nonzero = nonzero || value != 0
	}
	if c.function == azcosmos.VectorDistanceFunctionCosine && !nonzero {
		return errors.New("azurecosmos: cosine vector became all zero in float32")
	}
	return nil
}

func (c containerSchema) score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, errors.New("azurecosmos: native score must be finite")
	}
	switch c.function {
	case azcosmos.VectorDistanceFunctionCosine:
		if raw < -1 || raw > 1 {
			return 0, errors.New("azurecosmos: cosine similarity is outside [-1,1]")
		}
		return vectorstore.ScoreFromCosineSimilarity(raw), nil
	case azcosmos.VectorDistanceFunctionDotProduct:
		return vectorstore.ScoreFromInnerProduct(raw), nil
	case azcosmos.VectorDistanceFunctionEuclidean:
		if raw < 0 {
			return 0, errors.New("azurecosmos: Euclidean distance is negative")
		}
		return vectorstore.ScoreFromDistance(raw), nil
	default:
		return 0, ErrIncompatibleContainer
	}
}

func (c containerSchema) decodeDocument(raw []byte, partition string) (*document.Document, azcore.ETag, error) {
	var record itemRecord
	if err := jsonv2.Unmarshal(raw, &record, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, "", err
	}
	if record.ID == nil || record.PartitionKey == nil || *record.PartitionKey != partition || record.Content == nil || record.Metadata == nil || record.Embedding == nil || record.ETag == nil || *record.ETag == "" || *record.ETag == "*" || strings.ContainsAny(*record.ETag, "\r\n") {
		return nil, "", errors.New("azurecosmos: item violates the strict current schema, partition or ETag contract")
	}
	if err := validateID(*record.ID); err != nil {
		return nil, "", err
	}
	if err := c.validateVector(*record.Embedding); err != nil {
		return nil, "", err
	}
	var facts metadata.Map
	if err := facts.UnmarshalJSON([]byte(*record.Metadata)); err != nil {
		return nil, "", err
	}
	doc := &document.Document{ID: *record.ID, Text: *record.Content, Metadata: facts}
	if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return nil, "", err
	}
	return doc, azcore.ETag(*record.ETag), nil
}
