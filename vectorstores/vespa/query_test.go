package vespa

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func newQueryTestStore(t *testing.T, body string) *Store {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			t.Errorf("deleted %s after an incomplete query", request.URL.Path)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Errorf("write query response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return &Store{
		endpoint:   server.URL,
		schemaName: "document",
		namespace:  "scope",
		idField:    "doc_id",
		httpClient: server.Client(),
	}
}

// Vespa enables soft timeout by default, so a partially evaluated query answers
// with 200 and a degraded coverage report. Accepting those hits would shrink a
// search result and leave matching documents behind during filtered deletion.
func TestQueryRequiresFullCoverage(t *testing.T) {
	for _, sample := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "full coverage",
			body: `{"root":{"coverage":{"coverage":100,"full":true},"children":[]}}`,
		},
		{
			name: "degraded by timeout",
			body: `{"root":{"coverage":{"coverage":99,"full":false,` +
				`"degraded":{"timeout":true,"match-phase":false}},"children":[]}}`,
			want: "query evaluated 99% of the corpus, degraded by timeout",
		},
		{
			name: "degraded by several causes",
			body: `{"root":{"coverage":{"coverage":40,"full":false,` +
				`"degraded":{"adaptive-timeout":true,"non-ideal-state":true}},"children":[]}}`,
			want: "degraded by adaptive-timeout,non-ideal-state",
		},
		{
			name: "degraded without a stated cause",
			body: `{"root":{"coverage":{"coverage":80,"full":false},"children":[]}}`,
			want: "degraded by unspecified",
		},
		{
			name: "missing coverage report",
			body: `{"root":{"children":[]}}`,
			want: "query response is missing its coverage report",
		},
		{
			name: "reported error",
			body: `{"root":{"errors":[{"code":12,"summary":"Timed out",` +
				`"message":"Request timed out after 500 ms"}],"coverage":{"coverage":0,"full":false}}}`,
			want: "query reported error 12: Timed out: Request timed out after 500 ms",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			store := newQueryTestStore(t, sample.body)
			hits, err := store.query(t.Context(), map[string]any{"yql": "select * from document where true"})
			if sample.want == "" {
				if err != nil {
					t.Fatalf("query() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("query() = %v, want an error containing %q", err, sample.want)
			}
			if hits != nil {
				t.Fatalf("query() returned %d hits alongside an error", len(hits))
			}
		})
	}
}

// A degraded enumeration must not be mistaken for an exhausted one, because
// DeleteWhere ends its loop as soon as a page comes back without hits.
func TestDeleteWhereRejectsDegradedEnumeration(t *testing.T) {
	store := newQueryTestStore(t, `{"root":{"coverage":{"coverage":50,"full":false,`+
		`"degraded":{"timeout":true}},"children":[]}}`)
	predicate, err := filter.Parse(`tenant == 'scope'`)
	if err != nil {
		t.Fatal(err)
	}
	err = store.DeleteWhere(t.Context(), predicate)
	if err == nil || !strings.Contains(err.Error(), "vespa: enumerate ids: query evaluated 50%") {
		t.Fatalf("DeleteWhere() = %v, want a degraded enumeration error", err)
	}
}

// Metadata leaves the store as raw JSON so a stored integer beyond the exact
// float64 range survives; decoding through map[string]any would round it.
func TestToDocumentPreservesLargeIntegerMetadata(t *testing.T) {
	t.Parallel()

	store := &Store{contentField: "content", embeddingField: "embedding", idField: "doc_id"}
	var fields metadata.Map
	if err := json.Unmarshal(
		[]byte(`{"doc_id":"one","content":"hello","embedding":[0.1],"ordinal":9007199254740993}`),
		&fields,
	); err != nil {
		t.Fatal(err)
	}
	doc, err := store.toDocument("id:scope:document::one", fields)
	if err != nil {
		t.Fatal(err)
	}
	if doc.ID != "one" || doc.Text != "hello" {
		t.Fatalf("document = %+v", doc)
	}
	if _, present := doc.Metadata["embedding"]; present {
		t.Fatal("metadata retained the embedding field")
	}
	if got := string(doc.Metadata["ordinal"]); got != "9007199254740993" {
		t.Fatalf("ordinal = %s, want 9007199254740993", got)
	}
}
