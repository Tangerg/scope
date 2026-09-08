package elasticsearch

import (
	"io"
	"strings"
	"testing"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// Elasticsearch reports skipped documents inside a 200 delete_by_query
// response, so a successful status alone never establishes that the filter was
// fully applied.
func TestDeleteByQueryRequiresCompleteDeletion(t *testing.T) {
	for _, sample := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "complete",
			body: `{"timed_out":false,"total":2,"deleted":2,"version_conflicts":0,"failures":[]}`,
		},
		{
			name: "nothing matched",
			body: `{"timed_out":false,"total":0,"deleted":0,"version_conflicts":0,"failures":[]}`,
		},
		{
			name: "version conflicts",
			body: `{"timed_out":false,"total":8,"deleted":0,"version_conflicts":8,"failures":[]}`,
			want: "left 8 document(s) on version conflict",
		},
		{
			name: "timed out",
			body: `{"timed_out":true,"total":9,"deleted":4,"version_conflicts":0,"failures":[]}`,
			want: "timed out after deleting 4 of 9 document(s)",
		},
		{
			name: "short deletion",
			body: `{"timed_out":false,"total":5,"deleted":3,"version_conflicts":0,"failures":[]}`,
			want: "deleted 3 of 5 matched document(s)",
		},
		{
			name: "per document failure",
			body: `{"timed_out":false,"total":2,"deleted":1,"version_conflicts":0,` +
				`"failures":[{"id":"two","status":403,"cause":{"reason":"action unauthorized"}}]}`,
			want: `failed for document "two" with status 403: action unauthorized`,
		},
		{
			name: "failure without a reason",
			body: `{"timed_out":false,"total":1,"deleted":0,"version_conflicts":0,` +
				`"failures":[{"id":"one","status":500,"cause":{}}]}`,
			want: `failed for document "one" with status 500: provider returned no reason`,
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store := &Store{indexName: "documents"}
			err := store.parseDeleteByQueryResponse(&esapi.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(sample.body)),
			})
			if sample.want == "" {
				if err != nil {
					t.Fatalf("parseDeleteByQueryResponse() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("parseDeleteByQueryResponse() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}

func TestDeleteByQueryRejectsUndecodableResponse(t *testing.T) {
	store := &Store{indexName: "documents"}
	err := store.parseDeleteByQueryResponse(&esapi.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"total":`)),
	})
	if err == nil || !strings.Contains(err.Error(), "decode delete_by_query response") {
		t.Fatalf("parseDeleteByQueryResponse() = %v, want a decode error", err)
	}
}
