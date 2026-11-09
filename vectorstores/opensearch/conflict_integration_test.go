//go:build integration

package opensearch

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	opensearchsdk "github.com/opensearch-project/opensearch-go/v5"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type concurrentWriter struct {
	client *opensearchsdk.Client
	index  string
	done   bool
}

func (c *concurrentWriter) Stream(request *http.Request) (*http.Response, error) {
	if request.URL.Path == "/_bulk" && !c.done {
		c.done = true
		if err := nativeCall(request.Context(), c.client, http.MethodPut, "/"+c.index+"/_doc/one", []byte(`{"content":"updated","embedding":[1,0],"metadata_json":"{\"value\":\"y\"}"}`)); err != nil {
			return nil, err
		}
	}
	return c.client.Stream(request)
}

func TestLiveConditionalDeleteRetainsConcurrentVersion(t *testing.T) {
	store, client := liveStore(t, liveConfig{engine: "lucene", metric: "cosinesimil"})
	facts, err := metadata.FromValues(map[string]any{"value": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text", Metadata: facts}})); err != nil {
		t.Fatal(err)
	}
	if err = refreshNative(t.Context(), client, store.indexName); err != nil {
		t.Fatal(err)
	}
	store.client = fixtureProtocol(t, &concurrentWriter{client: client, index: store.indexName}, 0)
	if err = store.DeleteMatching(t.Context(), filter.EQ("value", "x"), fixtureBudget()); err == nil {
		t.Fatal("stale native CAS reported success")
	}
	if err = refreshNative(t.Context(), client, store.indexName); err != nil {
		t.Fatal(err)
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")})
	if err != nil || len(response.Results) != 1 || string(response.Results[0].Document.Metadata["value"]) != `"y"` || response.Results[0].Document.Text != "updated" {
		t.Fatalf("concurrent version was lost: %#v, %v", response, err)
	}
	if err = store.DeleteMatching(t.Context(), filter.EQ("value", "y"), fixtureBudget()); err != nil {
		t.Fatal(err)
	}
}

func TestLiveFilteredGroupsPreserveNativeRanking(t *testing.T) {
	store, client := liveStore(t, liveConfig{engine: "lucene", metric: "innerproduct"})
	docs := make([]*document.Document, 513)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%04d", i), Text: strconv.Itoa(i + 1)}
	}
	batch := fixtureBatch(docs)
	for i := range batch.Records {
		batch.Records[i].Values[0] = float64(i + 1)
	}
	if err := store.Upsert(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if err := refreshNative(t.Context(), client, store.indexName); err != nil {
		t.Fatal(err)
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("1"), Options: vectorstore.SearchOptions{TopK: 3, Filter: filter.IsNull("unused")}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, hit := range response.Results {
		ids = append(ids, hit.Document.ID)
	}
	if strings.Join(ids, ",") != "0512,0511,0510" {
		t.Fatalf("native ranking changed across groups: %v", ids)
	}
}
