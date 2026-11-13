package milvus

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/milvus-io/milvus-proto/go-api/v3/commonpb"
	"github.com/milvus-io/milvus/client/v3/entity"
	"github.com/milvus-io/milvus/client/v3/index"
	"github.com/milvus-io/milvus/client/v3/milvusclient"
	"github.com/samber/lo"

	corevector "github.com/Tangerg/scope/core/vector"
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
	fields, err := currentFields(collection, name)
	if err != nil {
		return nativePolicy{}, err
	}
	policy := nativePolicy{}
	for field, target := range map[string]*int{fieldID: &policy.idBytes, fieldContent: &policy.contentBytes, fieldMeta: &policy.metadataBytes} {
		if *target, err = varCharCapacity(fields[field], field); err != nil {
			return nativePolicy{}, err
		}
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

// currentFields admits exactly the four current fields with caller-supplied
// IDs and vectors, keyed by name.
func currentFields(collection *entity.Collection, name string) (map[string]*entity.Field, error) {
	if collection == nil || collection.Schema == nil {
		return nil, fmt.Errorf("%w: missing collection", ErrSchemaMismatch)
	}
	schema := collection.Schema
	if collection.Name != name || schema.CollectionName != name || schema.AutoID || schema.EnableDynamicField || len(schema.Functions) != 0 || len(schema.Fields) != 4 {
		return nil, fmt.Errorf("%w: collection must have exactly the four current fields and caller-supplied IDs and vectors", ErrSchemaMismatch)
	}
	fields := make(map[string]*entity.Field, len(schema.Fields))
	for _, field := range schema.Fields {
		if field == nil || fields[field.Name] != nil || !plainField(field) {
			return nil, fmt.Errorf("%w: invalid field policy", ErrSchemaMismatch)
		}
		fields[field.Name] = field
	}
	return fields, nil
}

// plainField rejects every native field behavior that would let Milvus
// originate or reinterpret a value Core supplies.
func plainField(field *entity.Field) bool {
	return !field.Nullable && !field.AutoID && !field.IsDynamic && !field.IsPartitionKey && !field.IsClusteringKey &&
		field.DefaultValue == nil && field.StructSchema == nil && field.PrimaryKey == (field.Name == fieldID)
}

func varCharCapacity(field *entity.Field, name string) (int, error) {
	if field == nil || field.DataType != entity.FieldTypeVarChar {
		return 0, fmt.Errorf("%w: %s must be VARCHAR", ErrSchemaMismatch, name)
	}
	capacity, err := strconv.Atoi(field.TypeParams[entity.TypeParamMaxLength])
	if err != nil || capacity < 1 || capacity > nativeMaxVarCharBytes {
		return 0, fmt.Errorf("%w: invalid %s capacity", ErrSchemaMismatch, name)
	}
	return capacity, nil
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
	if err := vectorstore.RequireWidth(values, n.dimensions); err != nil {
		return nil, err
	}
	result := corevector.Float32(values)
	if err := n.validateVector(result); err != nil {
		return nil, err
	}
	return result, nil
}

func (n nativePolicy) validateVector(values []float32) error {
	if err := corevector.Coordinates[float32](values).Validate(); err != nil {
		return err
	}
	if len(values) != n.dimensions {
		return errors.New("milvus: native vector width is inconsistent")
	}
	if n.metric == entity.COSINE {
		return corevector.Coordinates[float32](values).ValidateDirection()
	}
	return nil
}

// compareRaw orders native scores best first: L2 is a distance, while cosine
// and inner product are similarities.
func (n nativePolicy) compareRaw(left, right float64) int {
	if n.metric == entity.L2 {
		return cmp.Compare(left, right)
	}
	return cmp.Compare(right, left)
}

func (n nativePolicy) score(raw float64) (vectorstore.Score, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, errors.New("milvus: native score is not finite")
	}
	switch n.metric {
	case entity.COSINE:
		score := vectorstore.ScoreFromCosineSimilarity(raw)
		return score, score.Validate()
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
