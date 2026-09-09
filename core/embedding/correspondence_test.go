package embedding_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
)

func outputs(t *testing.T, vectors ...[]float64) []*embedding.Output {
	t.Helper()
	built := make([]*embedding.Output, 0, len(vectors))
	for _, vector := range vectors {
		output, err := embedding.NewOutput(vector, nil)
		if err != nil {
			t.Fatal(err)
		}
		built = append(built, output)
	}
	return built
}

// Outputs declares one entry per input text, and that pairing is the whole
// basis for using an embedding: one vector short leaves every later text
// carrying its neighbor's vector with nothing downstream able to notice.
func TestValidateForRequiresOneOutputPerInput(t *testing.T) {
	t.Parallel()

	request, err := embedding.NewRequest([]string{"first", "second", "third"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name    string
		vectors [][]float64
		want    string
	}{
		{
			name:    "one per input",
			vectors: [][]float64{{1, 0}, {0, 1}, {1, 1}},
		},
		{
			name:    "one short",
			vectors: [][]float64{{1, 0}, {0, 1}},
			want:    "got 2 outputs for 3 input texts",
		},
		{
			name:    "one extra",
			vectors: [][]float64{{1, 0}, {0, 1}, {1, 1}, {0, 0}},
			want:    "got 4 outputs for 3 input texts",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			response := &embedding.Response{
				Outputs:  outputs(t, sample.vectors...),
				Metadata: &embedding.ResponseMetadata{Model: "model"},
			}
			err := response.ValidateFor(request)
			if sample.want == "" {
				if err != nil {
					t.Fatalf("ValidateFor() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("ValidateFor() = %v, want an error containing %q", err, sample.want)
			}
			if !errors.Is(err, embedding.ErrInvalidResponse) {
				t.Fatalf("ValidateFor() error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestValidateForRejectsMalformedValuesWithMatchingCounts(t *testing.T) {
	validRequest := &embedding.Request{Texts: []string{"input"}}
	validResponse := &embedding.Response{Outputs: outputs(t, []float64{1})}
	for _, test := range []struct {
		name     string
		request  *embedding.Request
		response *embedding.Response
		want     error
	}{
		{"empty input", &embedding.Request{Texts: []string{""}}, validResponse, embedding.ErrInvalidRequest},
		{"empty vector", validRequest, &embedding.Response{Outputs: []*embedding.Output{{}}}, embedding.ErrInvalidResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.response.ValidateFor(test.request); !errors.Is(err, test.want) {
				t.Fatalf("ValidateFor = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPlaceOutputRejectsInvalidVectorWithoutClaimingPosition(t *testing.T) {
	placed := make([]*embedding.Output, 1)
	if err := embedding.PlaceOutput(placed, 0, nil, nil); !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("PlaceOutput = %v, want ErrInvalidResponse", err)
	}
	if placed[0] != nil {
		t.Fatal("rejected vector claimed an output position")
	}
	if err := embedding.PlaceOutput(placed, 0, []float64{1}, nil); err != nil {
		t.Fatalf("valid replacement: %v", err)
	}
}

// A provider that tags each embedding with its own index may answer out of
// order. Appending in arrival order would pair texts with the wrong vectors,
// so placement is by index.
func TestPlaceOutputRestoresRequestOrder(t *testing.T) {
	t.Parallel()

	placed := make([]*embedding.Output, 3)
	for _, item := range []struct {
		index  int
		vector []float64
	}{
		{index: 2, vector: []float64{3}},
		{index: 0, vector: []float64{1}},
		{index: 1, vector: []float64{2}},
	} {
		if err := embedding.PlaceOutput(placed, item.index, item.vector, nil); err != nil {
			t.Fatalf("PlaceOutput(%d) = %v, want nil", item.index, err)
		}
	}
	for index, output := range placed {
		if want := float64(index + 1); output.Embedding[0] != want {
			t.Fatalf("placed[%d] = %v, want %v", index, output.Embedding[0], want)
		}
	}
}

// An index the request cannot hold, or a position claimed twice, means the
// reply does not describe the request that was sent.
func TestPlaceOutputRejectsPositionsTheRequestCannotHold(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name  string
		place []int
		want  string
	}{
		{name: "negative", place: []int{-1}, want: "index -1 is out of range for 2 inputs"},
		{name: "past the end", place: []int{2}, want: "index 2 is out of range for 2 inputs"},
		{name: "repeated", place: []int{0, 0}, want: "index 0 appears more than once"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			placed := make([]*embedding.Output, 2)
			var err error
			for _, index := range sample.place {
				err = embedding.PlaceOutput(placed, index, []float64{1}, nil)
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("PlaceOutput() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}

// An input the provider never answered leaves a hole, and building the
// Response names the text that went unanswered.
func TestUnansweredInputFailsWhenTheResponseIsBuilt(t *testing.T) {
	t.Parallel()

	placed := make([]*embedding.Output, 3)
	if err := embedding.PlaceOutput(placed, 0, []float64{1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := embedding.PlaceOutput(placed, 2, []float64{3}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := embedding.NewResponse(placed, &embedding.ResponseMetadata{Model: "model"})
	if err == nil || !strings.Contains(err.Error(), "outputs[1]") {
		t.Fatalf("NewResponse() = %v, want an error naming outputs[1]", err)
	}
}
