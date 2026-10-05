package qdrant

import (
	"context"
	"errors"
	"math"
	"testing"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/vectorstore"
)

type policyClient struct {
	APIClient
	info    *qdrantclient.CollectionInfo
	names   []string
	failure error
}

func (p *policyClient) ListCollections(context.Context) ([]string, error) { return p.names, p.failure }
func (p *policyClient) GetCollectionInfo(context.Context, string) (*qdrantclient.CollectionInfo, error) {
	return p.info, p.failure
}
func (p *policyClient) ScrollAndOffset(context.Context, *qdrantclient.ScrollPoints) ([]*qdrantclient.RetrievedPoint, *qdrantclient.PointId, error) {
	return nil, nil, p.failure
}

func TestNativeCollectionPolicyHasNoConfiguredMetricOrDimension(t *testing.T) {
	for _, metric := range []qdrantclient.Distance{qdrantclient.Distance_Cosine, qdrantclient.Distance_Dot, qdrantclient.Distance_Euclid, qdrantclient.Distance_Manhattan} {
		client := &policyClient{info: collectionInfo(metric, 2), names: []string{"documents"}}
		model := embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
			t.Fatal("construction called model")
			return nil, nil
		})
		store, err := NewStore(t.Context(), StoreConfig{Client: client, CollectionName: "documents", EmbeddingModel: model, DocumentBatcher: visibilityBatcher{}})
		if err != nil || store.schema.dimensions != 2 || store.schema.metric != metric {
			t.Fatalf("store=%v err=%v", store, err)
		}
	}
	for name, mutate := range map[string]func(*qdrantclient.CollectionInfo){
		"missing config": func(info *qdrantclient.CollectionInfo) { info.Config = nil },
		"missing vector": func(info *qdrantclient.CollectionInfo) { info.Config.Params.VectorsConfig = nil },
		"zero dimension": func(info *qdrantclient.CollectionInfo) { info.Config.Params.VectorsConfig.GetParams().Size = 0 },
		"overflow dimension": func(info *qdrantclient.CollectionInfo) {
			info.Config.Params.VectorsConfig.GetParams().Size = math.MaxUint64
		},
		"unknown metric": func(info *qdrantclient.CollectionInfo) {
			info.Config.Params.VectorsConfig.GetParams().Distance = qdrantclient.Distance_UnknownDistance
		},
		"FLOAT16": func(info *qdrantclient.CollectionInfo) {
			info.Config.Params.VectorsConfig.GetParams().Datatype = new(qdrantclient.Datatype_Float16)
		},
		"named vector": func(info *qdrantclient.CollectionInfo) {
			info.Config.Params.VectorsConfig = qdrantclient.NewVectorsConfigMap(map[string]*qdrantclient.VectorParams{"named": {Size: 2, Distance: qdrantclient.Distance_Cosine}})
		},
		"multivector": func(info *qdrantclient.CollectionInfo) {
			info.Config.Params.VectorsConfig.GetParams().MultivectorConfig = &qdrantclient.MultiVectorConfig{}
		},
		"custom sharding": func(info *qdrantclient.CollectionInfo) {
			info.Config.Params.ShardingMethod = new(qdrantclient.ShardingMethod_Custom)
		},
	} {
		t.Run(name, func(t *testing.T) {
			info := collectionInfo(qdrantclient.Distance_Cosine, 2)
			mutate(info)
			var schema nativeSchema
			if err := schema.read(info); !errors.Is(err, ErrIncompatibleCollection) {
				t.Fatal(err)
			}
		})
	}
	var schema nativeSchema
	if err := schema.read(nil); !errors.Is(err, ErrIncompatibleCollection) {
		t.Fatal(err)
	}
}

func TestConstructionRejectsMissingPolicyAndInvalidConfig(t *testing.T) {
	client := &policyClient{info: collectionInfo(qdrantclient.Distance_Cosine, 2), names: []string{"documents"}}
	config := StoreConfig{Client: client, CollectionName: "documents", EmbeddingModel: embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) { return nil, nil }), DocumentBatcher: visibilityBatcher{}}
	for _, mutate := range []func(*StoreConfig){func(c *StoreConfig) { c.Client = (*policyClient)(nil) }, func(c *StoreConfig) { c.CollectionName = "" }, func(c *StoreConfig) { c.EmbeddingModel = nil }, func(c *StoreConfig) { c.DocumentBatcher = nil }} {
		candidate := config
		mutate(&candidate)
		if store, err := NewStore(t.Context(), candidate); err == nil || store != nil {
			t.Fatal("invalid config reached native API")
		}
	}
	config.CollectionName = "alias"
	if store, err := NewStore(t.Context(), config); !errors.Is(err, ErrIncompatibleCollection) || store != nil {
		t.Fatalf("alias became a concrete collection: %v", err)
	}
	config.CollectionName = "documents"
	client.failure = errors.New("native policy denied")
	if store, err := NewStore(t.Context(), config); !errors.Is(err, client.failure) || store != nil {
		t.Fatalf("control failure invented schema: %v", err)
	}
}

func TestNativeScoreDelegatesOnlyNormalizationToCore(t *testing.T) {
	for _, sample := range []struct {
		metric qdrantclient.Distance
		raw    float64
		want   vectorstore.Score
	}{{qdrantclient.Distance_Cosine, 1, 1}, {qdrantclient.Distance_Dot, 0, 0.5}, {qdrantclient.Distance_Euclid, 4, 0.2}, {qdrantclient.Distance_Manhattan, 3, 0.25}} {
		got, err := (nativeSchema{metric: sample.metric}).score(sample.raw)
		if err != nil || got != sample.want {
			t.Fatalf("score=%v err=%v", got, err)
		}
	}
	if _, err := (nativeSchema{}).score(1); err == nil {
		t.Fatal("missing metric succeeded")
	}
	for _, raw := range []float64{math.NaN(), math.Inf(1)} {
		if _, err := (nativeSchema{metric: qdrantclient.Distance_Cosine}).score(raw); err == nil {
			t.Fatal("nonfinite score succeeded")
		}
	}
}
