package weaviate

import (
	"errors"
	"strings"
	"testing"

	"github.com/weaviate/weaviate/entities/models"
)

func batchResult(status string, failure error) *models.ObjectsGetResponseAO2Result {
	result := &models.ObjectsGetResponseAO2Result{}
	if status != "" {
		result.Status = &status
	}
	if failure != nil {
		result.Errors = &models.ErrorResponse{
			Error: []*models.ErrorResponseErrorItems0{{Message: failure.Error()}},
		}
	}
	return result
}

// Weaviate answers a batch whose objects individually failed with a successful
// HTTP call, so only the per-object results establish that the batch applied.
func TestIndexRequiresEveryObjectAcknowledgment(t *testing.T) {
	t.Parallel()

	objects := []*models.Object{{ID: "one"}, {ID: "two"}}
	success := models.ObjectsGetResponseAO2ResultStatusSUCCESS
	for _, sample := range []struct {
		name      string
		responses []models.ObjectsGetResponse
		want      string
	}{
		{
			name: "all accepted",
			responses: []models.ObjectsGetResponse{
				{Object: models.Object{ID: "one"}, Result: batchResult(success, nil)},
				{Object: models.Object{ID: "two"}, Result: batchResult(success, nil)},
			},
		},
		{
			name: "one reported an error",
			responses: []models.ObjectsGetResponse{
				{Object: models.Object{ID: "one"}, Result: batchResult(success, nil)},
				{Object: models.Object{ID: "two"}, Result: batchResult(
					models.ObjectsGetResponseAO2ResultStatusFAILED, errTestShardDown)},
			},
			want: "batch insert error for object two",
		},
		{
			name: "failed status without an error payload",
			responses: []models.ObjectsGetResponse{
				{Object: models.Object{ID: "one"}, Result: batchResult(success, nil)},
				{Object: models.Object{ID: "two"}, Result: batchResult(
					models.ObjectsGetResponseAO2ResultStatusFAILED, nil)},
			},
			want: "object two reported status FAILED",
		},
		{
			name: "missing status",
			responses: []models.ObjectsGetResponse{
				{Object: models.Object{ID: "one"}, Result: batchResult(success, nil)},
				{Object: models.Object{ID: "two"}, Result: batchResult("", nil)},
			},
			want: "object two reported status none",
		},
		{
			name: "missing result",
			responses: []models.ObjectsGetResponse{
				{Object: models.Object{ID: "one"}, Result: batchResult(success, nil)},
				{Object: models.Object{ID: "two"}},
			},
			want: "no result for object two",
		},
		{
			name: "fewer results than objects",
			responses: []models.ObjectsGetResponse{
				{Object: models.Object{ID: "one"}, Result: batchResult(success, nil)},
			},
			want: "returned 1 results for 2 objects",
		},
		{
			name: "more results than objects",
			responses: []models.ObjectsGetResponse{
				{Object: models.Object{ID: "one"}, Result: batchResult(success, nil)},
				{Object: models.Object{ID: "two"}, Result: batchResult(success, nil)},
				{Object: models.Object{ID: "three"}, Result: batchResult(success, nil)},
			},
			want: "returned 3 results for 2 objects",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			err := checkBatchAcknowledgments(objects, sample.responses)
			if sample.want == "" {
				if err != nil {
					t.Fatalf("checkBatchAcknowledgments() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("checkBatchAcknowledgments() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}

var errTestShardDown = errors.New("shard is not ready")
