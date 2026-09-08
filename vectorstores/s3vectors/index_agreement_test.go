package s3vectors

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
)

// describeIndexClient answers GetIndex with a scripted index. Unused
// operations stay unimplemented so a call to one fails the test rather than
// passing silently.
type describeIndexClient struct {
	VectorClient
	index *types.Index
}

func (d *describeIndexClient) GetIndex(
	_ context.Context,
	_ *s3vectors.GetIndexInput,
	_ ...func(*s3vectors.Options),
) (*s3vectors.GetIndexOutput, error) {
	return &s3vectors.GetIndexOutput{Index: d.index}, nil
}

// QueryVectors answers with a raw distance and nothing that says which metric
// produced it, so a configured metric that disagrees with the index does not
// fail -- it rescales every score. The comment on DistanceMetric used to say so
// outright: "the store doesn't enforce consistency". MinScore then filters by a
// threshold in the wrong scale, and the ranked output stays plausible.
func TestValidateIndexMetricRefusesADisagreeingIndex(t *testing.T) {
	t.Parallel()

	index := &types.Index{
		IndexName:      aws.String("documents"),
		DistanceMetric: types.DistanceMetricEuclidean,
		Dimension:      aws.Int32(2),
	}

	err := validateIndexMetric(index, "documents", DistanceCosine)
	if !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatalf("validateIndexMetric() = %v, want ErrIncompatibleIndex", err)
	}
	if err = validateIndexMetric(index, "documents", DistanceEuclidean); err != nil {
		t.Fatalf("validateIndexMetric() = %v, want nil for the index's own metric", err)
	}
}

// The check has to run where the misconfiguration is. NewStore takes a context
// for exactly this reason, so a caller learns at wiring instead of reading
// rescaled scores for the life of the store.
func TestNewStoreRefusesADisagreeingIndex(t *testing.T) {
	t.Parallel()

	client := &describeIndexClient{index: &types.Index{
		IndexName:      aws.String("documents"),
		DistanceMetric: types.DistanceMetricEuclidean,
		Dimension:      aws.Int32(2),
	}}

	_, err := NewStore(t.Context(), StoreConfig{
		Client:           client,
		VectorBucketName: "bucket",
		IndexName:        "documents",
		EmbeddingModel:   constantEmbeddingModel(),
		DocumentBatcher:  singleBatcher{},
		DistanceMetric:   DistanceCosine,
	})
	if !errors.Is(err, ErrIncompatibleIndex) {
		t.Fatalf("NewStore() = %v, want ErrIncompatibleIndex", err)
	}
}

// The metric vocabulary is a copy of the SDK's, so the two have to keep
// spelling the same thing; a rename on either side would otherwise turn every
// comparison above into a silent mismatch.
func TestDistanceMetricMatchesTheSDKVocabulary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		metric DistanceMetric
		sdk    types.DistanceMetric
	}{
		{metric: DistanceCosine, sdk: types.DistanceMetricCosine},
		{metric: DistanceEuclidean, sdk: types.DistanceMetricEuclidean},
	}

	for _, test := range tests {
		if string(test.metric) != string(test.sdk) {
			t.Errorf("DistanceMetric %q does not match SDK metric %q", test.metric, test.sdk)
		}
		if !test.metric.Valid() {
			t.Errorf("DistanceMetric %q is not accepted by Valid()", test.metric)
		}
	}
	if got := len(types.DistanceMetric("").Values()); got != len(tests) {
		t.Fatalf("SDK declares %d metrics, the store maps %d", got, len(tests))
	}
}

// singleBatcher satisfies the Batcher contract for a construction test, where
// no document is ever indexed.
type singleBatcher struct{}

func (singleBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	return [][]*document.Document{documents}, nil
}

func constantEmbeddingModel() embedding.Model {
	return embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for index := range outputs {
			outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
}
