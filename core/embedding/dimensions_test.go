package embedding_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
)

func dimensionRequest(t *testing.T, dimensions int64) *embedding.Request {
	t.Helper()

	request, err := embedding.NewRequest([]string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	request.Options.Dimensions = &dimensions
	return request
}

func dimensionResponse(t *testing.T, width int) *embedding.Response {
	t.Helper()

	outputs := make([]*embedding.Output, 0, 2)
	for range 2 {
		vector := make([]float64, width)
		for index := range vector {
			vector[index] = 0.5
		}
		output, err := embedding.NewOutput(vector, nil)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, output)
	}
	response, err := embedding.NewResponse(outputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestValidateForRequiresTheRequestedDimensions(t *testing.T) {
	t.Parallel()

	t.Run("honored", func(t *testing.T) {
		if err := dimensionResponse(t, 256).ValidateFor(dimensionRequest(t, 256)); err != nil {
			t.Fatalf("ValidateFor() = %v, want nil", err)
		}
	})

	t.Run("ignored by the model", func(t *testing.T) {
		err := dimensionResponse(t, 1536).ValidateFor(dimensionRequest(t, 256))
		if err == nil {
			t.Fatal("ValidateFor() = nil error, want a dimension mismatch")
		}
		if !strings.Contains(err.Error(), "asked for 256") {
			t.Fatalf("ValidateFor() = %v, want the requested size named", err)
		}
	})

	t.Run("unset", func(t *testing.T) {
		request, err := embedding.NewRequest([]string{"one", "two"})
		if err != nil {
			t.Fatal(err)
		}
		if err := dimensionResponse(t, 1536).ValidateFor(request); err != nil {
			t.Fatalf("ValidateFor() = %v, want nil", err)
		}
	})
}
