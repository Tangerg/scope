package moderation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/moderation"
)

func flaggedOutputs(t *testing.T, count int) []*moderation.Output {
	t.Helper()
	built := make([]*moderation.Output, 0, count)
	for range count {
		output, err := moderation.NewOutput(
			moderation.Categories{"hate": {Flagged: false, Score: 0.1}},
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		built = append(built, output)
	}
	return built
}

// Outputs declares one entry per input in the same order, and nothing on an
// Output ties it back to a text. A response one verdict short therefore leaves
// the last input unmoderated while every earlier verdict still looks well
// formed — the caller allows content nobody judged.
func TestValidateForRequiresOneVerdictPerInput(t *testing.T) {
	t.Parallel()

	request, err := moderation.NewRequest([]string{"first", "second", "third"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name  string
		count int
		want  string
	}{
		{name: "one per input", count: 3},
		{name: "one input unjudged", count: 2, want: "got 2 outputs for 3 inputs"},
		{name: "a verdict for no input", count: 4, want: "got 4 outputs for 3 inputs"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			response := &moderation.Response{
				Outputs:  flaggedOutputs(t, sample.count),
				Metadata: &moderation.ResponseMetadata{Model: "model"},
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
			if !errors.Is(err, moderation.ErrInvalidResponse) {
				t.Fatalf("ValidateFor() error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}
