package stability

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// A filtered generation still answers 200 with base64 bytes, and those bytes
// are the classifier's stand-in rather than the requested image. Returning
// them as an image would report a failure as a result.
func TestBuildResponseRejectsFilteredGeneration(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name   string
		reason string
		want   string
	}{
		{name: "success", reason: finishReasonSuccess},
		{name: "absent", reason: ""},
		{
			name:   "filtered",
			reason: finishReasonContentFiltered,
			want:   "filtered by the content moderation system",
		},
		{
			name:   "unrecognized",
			reason: "INVENTED_LATER",
			want:   `unrecognized finish_reason "INVENTED_LATER"`,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			body, err := json.Marshal(jsonResponse{
				Image:        base64.StdEncoding.EncodeToString([]byte("png-bytes")),
				FinishReason: sample.reason,
				Seed:         7,
			})
			if err != nil {
				t.Fatal(err)
			}

			response, err := new(ImageModel).buildResponse(body, nil, "png")
			if sample.want == "" {
				if err != nil {
					t.Fatalf("buildResponse() = %v, want nil", err)
				}
				if len(response.Outputs) != 1 {
					t.Fatalf("buildResponse() returned %d outputs, want 1", len(response.Outputs))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("buildResponse() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}
