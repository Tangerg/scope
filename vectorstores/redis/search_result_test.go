package redis

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestUnfilteredSearchOmitsNativeKeyRestriction(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 2)
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if err != nil || len(response.Results) != 2 {
		t.Fatalf("unfiltered search: %#v, %v", response, err)
	}
}

func TestNativeSearchEnvelopeRejectsPartialOrDamagedOutput(t *testing.T) {
	for _, fault := range []string{"RESP2", "warning missing", "warning malformed", "timeout", "format", "count missing", "count negative", "count lossy", "count incomplete", "results missing", "result malformed", "hit error", "field malformed", "late hit", "repeated key", "bad distance", "missing distance", "changed membership"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store(constantModel(), 1)
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text"}}}); err != nil {
				t.Fatal(err)
			}
			fields := make(map[string]any)
			for key, value := range fixture.rows["embedding:one"] {
				fields[key] = value
			}
			fields[distanceFieldName] = "0"
			hit := map[string]any{"id": "embedding:one", "extra_attributes": fields}
			reply := map[string]any{"format": "STRING", "warning": []any{}, "total_results": int64(1), "results": []any{hit}}
			fixture.searchReply = reply
			switch fault {
			case "RESP2":
				fixture.searchReply = []any{int64(0)}
			case "warning missing":
				delete(reply, "warning")
			case "warning malformed":
				reply["warning"] = "lost channel"
			case "timeout":
				reply["warning"] = []any{"query timed out"}
			case "format":
				reply["format"] = "EXPAND"
			case "count missing":
				delete(reply, "total_results")
			case "count negative":
				reply["total_results"] = int64(-1)
			case "count lossy":
				reply["total_results"] = float64(1)
			case "count incomplete":
				reply["total_results"] = int64(2)
			case "results missing":
				delete(reply, "results")
			case "result malformed":
				reply["results"] = []any{"lost hit"}
			case "hit error":
				hit["error"] = "document is gone"
			case "field malformed":
				fields[metadataField] = nil
			case "late hit":
				reply["total_results"] = int64(2)
				reply["results"] = []any{hit, map[string]any{"id": "embedding:two", "extra_attributes": map[string]any{contentField: "text"}}}
			case "repeated key":
				reply["total_results"] = int64(2)
				reply["results"] = []any{hit, hit}
			case "bad distance":
				fields[distanceFieldName] = "3"
			case "missing distance":
				delete(fields, distanceFieldName)
			case "changed membership":
				hit["id"] = "embedding:outside"
			}
			options := vectorstore.SearchOptions{TopK: 2}
			if fault == "changed membership" {
				options.Filter = filter.IsNull("unused")
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: options})
			if err == nil || response != nil {
				t.Fatalf("damaged native response succeeded: %#v, %v", response, err)
			}
			if fault == "hit error" && !strings.Contains(err.Error(), "document is gone") {
				t.Fatalf("native hit error disappeared: %v", err)
			}
		})
	}
}
