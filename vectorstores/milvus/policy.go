package milvus

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/milvus-io/milvus-proto/go-api/v2/commonpb"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

// A policy is an operation-local projection of the native collection and index.
// It cannot create, override or advance either native fact.
type nativePolicy struct {
	dimensions    int
	metric        entity.MetricType
	idBytes       int
	contentBytes  int
	metadataBytes int
}

func decodePolicy(collection *entity.Collection, name string) (nativePolicy, error) {
	if collection == nil || collection.Schema == nil {
		return nativePolicy{}, fmt.Errorf("%w: missing collection", ErrSchemaMismatch)
	}
	schema := collection.Schema
	if collection.Name != name || schema.CollectionName != name || schema.AutoID || schema.EnableDynamicField || len(schema.Functions) != 0 || len(schema.Fields) != 4 {
		return nativePolicy{}, fmt.Errorf("%w: collection must have exactly the four current fields and caller-supplied IDs and vectors", ErrSchemaMismatch)
	}
	fields := make(map[string]*entity.Field, 4)
	for _, field := range schema.Fields {
		if field == nil || fields[field.Name] != nil || field.Nullable || field.AutoID || field.IsDynamic || field.IsPartitionKey || field.IsClusteringKey || field.DefaultValue != nil || field.StructSchema != nil || field.PrimaryKey != (field.Name == fieldID) {
			return nativePolicy{}, fmt.Errorf("%w: invalid field policy", ErrSchemaMismatch)
		}
		fields[field.Name] = field
	}
	policy := nativePolicy{}
	for name, target := range map[string]*int{fieldID: &policy.idBytes, fieldContent: &policy.contentBytes, fieldMeta: &policy.metadataBytes} {
		field := fields[name]
		if field == nil || field.DataType != entity.FieldTypeVarChar {
			return nativePolicy{}, fmt.Errorf("%w: %s must be VARCHAR", ErrSchemaMismatch, name)
		}
		capacity, err := strconv.Atoi(field.TypeParams[entity.TypeParamMaxLength])
		if err != nil || capacity < 1 || capacity > nativeMaxVarCharBytes {
			return nativePolicy{}, fmt.Errorf("%w: invalid %s capacity", ErrSchemaMismatch, name)
		}
		*target = capacity
	}
	vector := fields[fieldVector]
	if vector == nil || vector.DataType != entity.FieldTypeFloatVector {
		return nativePolicy{}, fmt.Errorf("%w: vector must be FLOAT_VECTOR", ErrSchemaMismatch)
	}
	width, err := vector.GetDim()
	if err != nil || width <= 0 || int64(int(width)) != width {
		return nativePolicy{}, fmt.Errorf("%w: invalid vector width", ErrSchemaMismatch)
	}
	policy.dimensions = int(width)
	return policy, nil
}

func (n *nativePolicy) readMetric(description milvusclient.IndexDescription) error {
	if lo.IsNil(description.Index) || description.State != index.IndexState(commonpb.IndexState_Finished) {
		return fmt.Errorf("%w: vector index is not ready", ErrSchemaMismatch)
	}
	metric := entity.MetricType(description.Params()[index.MetricTypeKey])
	switch metric {
	case entity.COSINE, entity.IP, entity.L2:
	default:
		return fmt.Errorf("%w: unsupported native vector metric %q", ErrSchemaMismatch, metric)
	}
	n.metric = metric
	return nil
}

func (n nativePolicy) vector(values []float64) ([]float32, error) {
	if len(values) != n.dimensions {
		return nil, fmt.Errorf("milvus: vector has width %d, want %d", len(values), n.dimensions)
	}
	result := embedding.Float32Vector(values)
	if err := n.validateVector(result); err != nil {
		return nil, err
	}
	return result, nil
}

func (n nativePolicy) validateVector(values []float32) error {
	if len(values) != n.dimensions {
		return errors.New("milvus: native vector width is inconsistent")
	}
	nonzero := false
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("milvus: vector must be finite in float32")
		}
		nonzero = nonzero || value != 0
	}
	if n.metric == entity.COSINE && !nonzero {
		return errors.New("milvus: cosine vector must be nonzero")
	}
	return nil
}

func (n nativePolicy) score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, errors.New("milvus: native score is not finite")
	}
	switch n.metric {
	case entity.COSINE:
		if raw < -1 || raw > 1 {
			return 0, errors.New("milvus: cosine similarity is outside [-1, 1]")
		}
		return vectorstore.ScoreFromCosineSimilarity(raw), nil
	case entity.IP:
		return vectorstore.ScoreFromInnerProduct(raw), nil
	case entity.L2:
		if raw < 0 {
			return 0, errors.New("milvus: squared L2 distance must not be negative")
		}
		return vectorstore.ScoreFromDistance(raw), nil
	default:
		return 0, errors.New("milvus: unknown native metric")
	}
}
