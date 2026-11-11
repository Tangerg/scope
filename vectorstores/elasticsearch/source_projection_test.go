package elasticsearch

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/vectorstores/protocol/elastic"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type sourceProjectionClient struct {
	client APIClient
	reads  int
}

func (s *sourceProjectionClient) Perform(request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/_search") {
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		var projection struct {
			Source struct {
				ExcludeVectors *bool `json:"exclude_vectors"`
			} `json:"_source"`
		}
		err = errors.Join(jsonv2.UnmarshalRead(body, &projection), body.Close())
		if err != nil {
			return nil, err
		}
		if projection.Source.ExcludeVectors == nil || *projection.Source.ExcludeVectors {
			return nil, errors.New("elasticsearch fixture: default source projection excludes vectors")
		}
		s.reads++
	}
	return s.client.Perform(request)
}

func TestReadsExplicitlyIncludeSourceVectors(t *testing.T) {
	for _, operation := range []struct {
		name  string
		reads int
	}{
		{name: "query", reads: 1},
		{name: "scan", reads: 2},
		{name: "delete", reads: 1},
	} {
		t.Run(operation.name, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fixture.settings.Defaults["index.mapping.exclude_source_vectors"] = "true"
			fixture.rows["one"] = elastic.StoredDocument{Content: "text", MetadataJSON: "null", Embedding: []float32{1, 0}}
			client := &sourceProjectionClient{client: fixture.client}
			store := fixture.storeThrough(client)
			query := &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}}
			var response *vectorstore.SearchResponse
			var err error
			switch operation.name {
			case "query":
				response, err = store.Query(t.Context(), query)
			case "scan":
				query.Options.Filter = filter.IsNull("tenant")
				response, err = store.QueryByScan(t.Context(), query, fixtureBudget())
			case "delete":
				err = store.DeleteMatching(t.Context(), filter.IsNull("tenant"), fixtureBudget())
			}
			if err != nil || client.reads != operation.reads {
				t.Fatalf("reads = %d, error = %v; want %d successful reads", client.reads, err, operation.reads)
			}
			if operation.name == "delete" {
				if len(fixture.rows) != 0 {
					t.Fatalf("remaining rows = %v", fixture.rows)
				}
				return
			}
			if response == nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" || response.Results[0].Document.Text != "text" {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}
