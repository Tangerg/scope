package redis

import (
	"context"
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"
)

// existingIndex answers FT._LIST with one index name and FT.INFO with a fixed
// vector attribute. The embedded interface stays nil because initialize must
// not reach any other command once the index is already there.
type existingIndex struct {
	goredis.UniversalClient
	name      string
	attribute goredis.FTAttribute
}

func (e *existingIndex) FT_List(ctx context.Context) *goredis.StringSliceCmd {
	command := goredis.NewStringSliceCmd(ctx, "ft._list")
	command.SetVal([]string{e.name})
	return command
}

func (e *existingIndex) FTInfo(ctx context.Context, index string) *goredis.FTInfoCmd {
	command := new(goredis.FTInfoCmd)
	command.SetVal(goredis.FTInfoResult{Attributes: []goredis.FTAttribute{e.attribute}})
	return command
}

// Existence is not agreement. Search converts RediSearch's distance into a
// Score with the metric from this store's own config, so an index that ranks by
// a different metric returns scores that are wrong rather than missing —
// nothing fails, the ranking is silently mis-scaled. initialize used to skip
// creation as soon as the name was taken, which accepted exactly that.
func TestInitializeChecksAnExistingIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		attribute goredis.FTAttribute
		wantErr   bool
	}{
		{
			name: "agrees",
			attribute: goredis.FTAttribute{
				Attribute: "embedding", Type: "VECTOR", DistanceMetric: "COSINE", Dim: 3,
			},
		},
		{
			name: "metric disagrees",
			attribute: goredis.FTAttribute{
				Attribute: "embedding", Type: "VECTOR", DistanceMetric: "L2", Dim: 3,
			},
			wantErr: true,
		},
		{
			name: "dimension disagrees",
			attribute: goredis.FTAttribute{
				Attribute: "embedding", Type: "VECTOR", DistanceMetric: "COSINE", Dim: 1536,
			},
			wantErr: true,
		},
		{
			// A vector field under another name means the index was built for
			// something else entirely.
			name: "no vector attribute under the configured name",
			attribute: goredis.FTAttribute{
				Attribute: "vector", Type: "VECTOR", DistanceMetric: "COSINE", Dim: 3,
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &Store{
				client:         &existingIndex{name: "documents", attribute: test.attribute},
				indexName:      "documents",
				embeddingField: "embedding",
				dimensions:     3,
				distanceMetric: DistanceCosine,
			}
			err := store.initialize(t.Context(), true)
			if test.wantErr {
				if err == nil {
					t.Fatal("initialize() = nil error, want an incompatibility error")
				}
				if !errors.Is(err, ErrIncompatibleIndex) {
					t.Fatalf("initialize() = %v, want %v", err, ErrIncompatibleIndex)
				}
				return
			}
			if err != nil {
				t.Fatalf("initialize() = %v, want nil", err)
			}
		})
	}
}
