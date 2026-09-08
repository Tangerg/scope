package azurecosmos

import (
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

// VectorDistance() answers with a raw number and nothing that says which
// function produced it, so a configured function that disagrees with the
// container does not fail -- it rescales every score, and MinScore then filters
// by a threshold in the wrong scale. The partition-key path is checked from the
// same read because it is the only other thing this store assumes about a
// container it did not create.
func TestValidateContainer(t *testing.T) {
	t.Parallel()

	agreeing := func() *azcosmos.ContainerProperties {
		return &azcosmos.ContainerProperties{
			ID:                     "vectors",
			PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/" + DefaultPartitionKeyField}},
			VectorEmbeddingPolicy: &azcosmos.VectorEmbeddingPolicy{
				VectorEmbeddings: []azcosmos.VectorEmbedding{{
					Path:             "/" + DefaultEmbeddingField,
					DataType:         azcosmos.VectorDataTypeFloat32,
					DistanceFunction: azcosmos.VectorDistanceFunctionCosine,
					Dimensions:       2,
				}},
			},
		}
	}

	tests := []struct {
		name       string
		properties func() *azcosmos.ContainerProperties
		want       DistanceFunction
		wantErr    bool
	}{
		{name: "agrees", properties: agreeing, want: DistanceCosine},
		{
			name: "distance function disagrees",
			properties: func() *azcosmos.ContainerProperties {
				properties := agreeing()
				properties.VectorEmbeddingPolicy.VectorEmbeddings[0].DistanceFunction = azcosmos.VectorDistanceFunctionEuclidean
				return properties
			},
			want:    DistanceCosine,
			wantErr: true,
		},
		{
			name: "partitioned on another path",
			properties: func() *azcosmos.ContainerProperties {
				properties := agreeing()
				properties.PartitionKeyDefinition = azcosmos.PartitionKeyDefinition{Paths: []string{"/tenant"}}
				return properties
			},
			want:    DistanceCosine,
			wantErr: true,
		},
		{
			name: "no vector embedding policy",
			properties: func() *azcosmos.ContainerProperties {
				properties := agreeing()
				properties.VectorEmbeddingPolicy = nil
				return properties
			},
			want:    DistanceCosine,
			wantErr: true,
		},
		{
			name: "vector embedded at another path",
			properties: func() *azcosmos.ContainerProperties {
				properties := agreeing()
				properties.VectorEmbeddingPolicy.VectorEmbeddings[0].Path = "/other"
				return properties
			},
			want:    DistanceCosine,
			wantErr: true,
		},
		{
			name:       "no properties",
			properties: func() *azcosmos.ContainerProperties { return nil },
			want:       DistanceCosine,
			wantErr:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateContainer(test.properties(), DefaultEmbeddingField, DefaultPartitionKeyField, test.want)
			if test.wantErr {
				if !errors.Is(err, ErrIncompatibleContainer) {
					t.Fatalf("validateContainer() = %v, want ErrIncompatibleContainer", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateContainer() = %v, want nil", err)
			}
		})
	}
}

// The function vocabulary is a copy of the SDK's, so the two have to keep
// spelling the same thing; a rename on either side would turn every comparison
// above into a silent mismatch.
func TestDistanceFunctionMatchesTheSDKVocabulary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		function DistanceFunction
		sdk      azcosmos.VectorDistanceFunction
	}{
		{function: DistanceCosine, sdk: azcosmos.VectorDistanceFunctionCosine},
		{function: DistanceDotProduct, sdk: azcosmos.VectorDistanceFunctionDotProduct},
		{function: DistanceEuclidean, sdk: azcosmos.VectorDistanceFunctionEuclidean},
	}

	for _, test := range tests {
		if string(test.function) != string(test.sdk) {
			t.Errorf("DistanceFunction %q does not match SDK function %q", test.function, test.sdk)
		}
		if !test.function.Valid() {
			t.Errorf("DistanceFunction %q is not accepted by Valid()", test.function)
		}
	}
}
