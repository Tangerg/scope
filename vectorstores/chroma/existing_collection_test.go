package chroma

import (
	"errors"
	"testing"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
)

// collectionSpace answers Metadata with a fixed hnsw:space, or with nothing at
// all when present is false. The embedded interfaces stay nil because the space
// check must not reach any other call.
type collectionSpace struct {
	v2.Collection
	metadata v2.CollectionMetadata
}

func (c *collectionSpace) Metadata() v2.CollectionMetadata { return c.metadata }

type spaceMetadata struct {
	v2.CollectionMetadata
	space   string
	present bool
}

func (s *spaceMetadata) GetString(key string) (string, bool) {
	if key != v2.HNSWSpace || !s.present {
		return "", false
	}
	return s.space, true
}

// Existence is not agreement, and here the create option does not make it so:
// GetOrCreateCollection returns an existing collection as it is and ignores the
// space this store asked for. Search converts Chroma's distance into a Score
// with the metric from this store's own config, so a collection that ranks by a
// different space returns scores that are wrong rather than missing — nothing
// fails, the ranking is silently mis-scaled.
func TestCheckCollectionSpace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured DistanceMetric
		metadata   v2.CollectionMetadata
		wantErr    bool
	}{
		{
			name:       "agrees",
			configured: DistanceCosine,
			metadata:   &spaceMetadata{space: "cosine", present: true},
		},
		{
			name:       "disagrees",
			configured: DistanceCosine,
			metadata:   &spaceMetadata{space: "l2", present: true},
			wantErr:    true,
		},
		{
			// Chroma omits the key when the collection uses its default, so the
			// omission has to read as l2 rather than as a refusal.
			name:       "omitted key is the Chroma default",
			configured: DistanceL2,
			metadata:   &spaceMetadata{present: false},
		},
		{
			name:       "omitted key still disagrees with a non-default",
			configured: DistanceCosine,
			metadata:   &spaceMetadata{present: false},
			wantErr:    true,
		},
		{
			name:       "absent metadata is the Chroma default",
			configured: DistanceL2,
			metadata:   nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &Store{
				collectionName: "documents",
				distanceMetric: test.configured,
				collection:     &collectionSpace{metadata: test.metadata},
			}
			err := store.checkCollectionSpace()
			if test.wantErr {
				if err == nil {
					t.Fatal("checkCollectionSpace() = nil error, want an incompatibility error")
				}
				if !errors.Is(err, ErrIncompatibleCollection) {
					t.Fatalf("checkCollectionSpace() = %v, want %v", err, ErrIncompatibleCollection)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkCollectionSpace() = %v, want nil", err)
			}
		})
	}
}
