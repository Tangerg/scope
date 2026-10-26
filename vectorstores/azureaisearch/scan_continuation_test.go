package azureaisearch

import (
	jsonv2 "encoding/json/v2"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestFilteredSearchCompletesNativeScanContinuation(t *testing.T) {
	for _, lateFailure := range []bool{false, true} {
		name := "matching document after empty page"
		if lateFailure {
			name = "invalid metadata after empty page"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fixture.put(&document.Document{ID: "one", Text: "text", Metadata: rawMetadata(`{"tenant":"one"}`)})
			fixture.scan = func(writer http.ResponseWriter, body metadata.Map) {
				if _, present := body["filter"]; present {
					_, _ = writer.Write([]byte(`{"value":[]}`))
					return
				}
				if _, continued := body["skip"]; !continued {
					_, _ = writer.Write([]byte(`{"value":[],"@search.nextPageParameters":{"orderby":"id asc","skip":1,"top":1000}}`))
					return
				}
				row := fixture.rows["one"].Clone()
				if lateFailure {
					row[DefaultMetadataField] = []byte(`"invalid JSON"`)
				}
				if err := jsonv2.MarshalWrite(writer, struct {
					Value []metadata.Map `json:"value"`
				}{Value: []metadata.Map{row}}); err != nil {
					t.Error(err)
				}
			}
			response, err := fixture.store().Search(t.Context(), &vectorstore.SearchRequest{
				Query: "query", Options: vectorstore.SearchOptions{Filter: filter.EQ("tenant", "one")},
			})
			if lateFailure {
				if err == nil || response != nil || fixture.modelCalls.Load() != 0 {
					t.Fatalf("incomplete scan hid invalid metadata: response=%v calls=%d error=%v", response, fixture.modelCalls.Load(), err)
				}
				return
			}
			if err != nil || response == nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
				t.Fatalf("scan continuation lost eligible document: response=%v error=%v", response, err)
			}
		})
	}
}
