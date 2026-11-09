package elasticsearch

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/vectorstores/protocol/elastic"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestSearchRejectsDamageWithoutPartialResponse(t *testing.T) {
	for _, fault := range []string{"timeout", "missing shards", "failed shard", "missing total", "missing hits", "missing source", "missing metadata", "shadow metadata", "missing vector", "null coordinate", "missing tokens", "routing", "wrong index", "wrong membership", "repeated ID", "missing score", "bad score", "late corrupt hit"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store()
			if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
				t.Fatal(err)
			}
			hit := fixture.hit("one")
			page := fixture.page([]elastic.Hit{hit}, 1, "")
			source := map[string]json.RawMessage{}
			if err := jsonv2.Unmarshal(hit.Source, &source); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "timeout":
				page.TimedOut = new(true)
			case "missing shards":
				page.Shards = nil
			case "failed shard":
				page.Shards.Failed = new(1)
			case "missing total":
				page.Hits.Total = nil
			case "missing hits":
				page.Hits.Hits = nil
			case "missing source":
				hit.Source = nil
			case "missing metadata":
				delete(source, metadataField)
			case "shadow metadata":
				source["metadata"] = json.RawMessage(`{}`)
			case "missing vector":
				delete(source, embeddingField)
			case "null coordinate":
				source[embeddingField] = json.RawMessage(`[1,null]`)
			case "missing tokens":
				hit.SeqNo = nil
			case "routing":
				hit.Fields = map[string][]json.RawMessage{"_routing": {json.RawMessage(`"tenant"`)}}
			case "wrong index":
				hit.Index = "other"
			case "wrong membership":
				hit.ID = "outside"
			case "missing score":
				hit.Score = nil
			case "bad score":
				hit.Score = new(float64(3))
			}
			if strings.Contains(fault, "metadata") || fault == "missing vector" || fault == "null coordinate" {
				var err error
				hit.Source, err = jsonv2.Marshal(source)
				if err != nil {
					t.Fatal(err)
				}
			}
			if page.Hits.Hits != nil {
				*page.Hits.Hits = []elastic.Hit{hit}
			}
			if fault == "repeated ID" {
				*page.Hits.Hits = append(*page.Hits.Hits, hit)
				page.Hits.Total.Value = new(int64(2))
			}
			if fault == "late corrupt hit" {
				hit.ID = "two"
				hit.Source = json.RawMessage(`{"content":"text"}`)
				*page.Hits.Hits = append(*page.Hits.Hits, hit)
				page.Hits.Total.Value = new(int64(2))
			}
			fixture.reply = page
			options := vectorstore.SearchOptions{TopK: 2}
			if fault == "wrong membership" {
				options.Filter = filter.IsNull("unused")
			}
			response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: options})
			if err == nil || response != nil {
				t.Fatalf("damaged output succeeded: %#v, %v", response, err)
			}
		})
	}
}

func TestNativeLimitsAndMalformedRecordsFailBeforeQuery(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.settings.Settings["index.max_result_window"] = "1"
	store := fixture.store()
	if response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 2}}); !errors.Is(err, vectorstore.ErrInvalidOptions) || response != nil {
		t.Fatalf("native window ignored: %#v, %v", response, err)
	}
	if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	record := fixture.rows["one"]
	record.MetadataJSON = ""
	fixture.rows["one"] = record
	if response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("absent")}}); err == nil || response != nil || len(fixture.groups) != 0 {
		t.Fatalf("malformed record reached native ranking: %#v, %v", response, err)
	}
	if err := store.DeleteIDs(t.Context(), []string{"one", ""}); err == nil || len(fixture.rows) != 1 {
		t.Fatal("invalid explicit deletion partially published")
	}
}
