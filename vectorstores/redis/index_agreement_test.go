package redis

import (
	"context"
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/embedding"
)

// searchIndexClient answers the two search commands construction issues and
// nothing else, so a call to any other operation fails the test rather than
// reaching a server.
type searchIndexClient struct {
	goredis.UniversalClient
	names      []string
	attributes []goredis.FTAttribute
	infoCalls  int
}

func (s *searchIndexClient) FT_List(ctx context.Context) *goredis.StringSliceCmd {
	command := goredis.NewStringSliceCmd(ctx)
	command.SetVal(s.names)
	return command
}

func (s *searchIndexClient) FTInfo(_ context.Context, _ string) *goredis.FTInfoCmd {
	s.infoCalls++
	command := &goredis.FTInfoCmd{}
	command.SetVal(goredis.FTInfoResult{Attributes: s.attributes})
	return command
}

func vectorAttribute(metric string, dimensions int) goredis.FTAttribute {
	return goredis.FTAttribute{
		Identifier:     DefaultEmbeddingField,
		Attribute:      DefaultEmbeddingField,
		Type:           "VECTOR",
		Algorithm:      "HNSW",
		DataType:       "FLOAT32",
		Dim:            dimensions,
		DistanceMetric: metric,
	}
}

func newAgreementStore(t *testing.T, client goredis.UniversalClient, config StoreConfig) (*Store, error) {
	t.Helper()

	config.Client = client
	config.EmbeddingModel = embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for index := range outputs {
			outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	config.DocumentBatcher = ownershipBatcher{}
	return NewStore(t.Context(), config)
}

// InitializeSchema answers "may I create a missing index". Whether the index
// found is the one this store was configured for is a different question, and
// it matters most for an index provisioned out of band -- exactly the case that
// flag turns off. The check used to sit inside the creation path, so a store
// attaching to a pre-provisioned index scored against an unverified metric, and
// a wrong one returns scores that are wrong rather than absent.
func TestExistingIndexIsCheckedWithoutInitializeSchema(t *testing.T) {
	t.Parallel()

	client := &searchIndexClient{
		names:      []string{DefaultIndexName},
		attributes: []goredis.FTAttribute{vectorAttribute("L2", 2)},
	}

	_, err := newAgreementStore(t, client, StoreConfig{DistanceMetric: DistanceCosine})
	if !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatalf("NewStore() = %v, want ErrIncompatibleIndex", err)
	}
	if client.infoCalls != 1 {
		t.Fatalf("FT.INFO calls = %d, want the existing index inspected once", client.infoCalls)
	}
}

// An index the store neither found nor may create cannot serve a single
// request, so saying so at wiring beats failing on the first search.
func TestMissingIndexWithoutInitializeSchemaIsRefused(t *testing.T) {
	t.Parallel()

	client := &searchIndexClient{names: []string{"someone-elses-index"}}

	_, err := newAgreementStore(t, client, StoreConfig{DistanceMetric: DistanceCosine})
	if !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatalf("NewStore() = %v, want ErrIncompatibleIndex", err)
	}
}

// Dimensions are required to create an index and optional to attach to one, so
// demanding a value while checking would make an out-of-band index unusable
// without repeating a fact the index already holds. The metric is compared
// either way.
func TestExistingIndexAgreesWithoutDeclaredDimensions(t *testing.T) {
	t.Parallel()

	client := &searchIndexClient{
		names:      []string{DefaultIndexName},
		attributes: []goredis.FTAttribute{vectorAttribute("COSINE", 1536)},
	}

	store, err := newAgreementStore(t, client, StoreConfig{DistanceMetric: DistanceCosine})
	if err != nil {
		t.Fatalf("NewStore() = %v, want nil", err)
	}
	if store == nil {
		t.Fatal("NewStore() returned no store")
	}
}
