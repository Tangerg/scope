package elasticsearch

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

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
			page := fixture.page([]searchHit{hit}, 1, "")
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
				*page.Hits.Hits = []searchHit{hit}
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

func TestBulkAcknowledgmentsCannotInventSuccess(t *testing.T) {
	for _, raw := range []string{
		`{"items":[]}`,
		`{"errors":false,"items":[]}`,
		`{"errors":false,"items":[{"index":{"_index":"documents","_id":"other","status":201}}]}`,
		`{"errors":false,"items":[{"index":{"_index":"other","_id":"one","status":201}}]}`,
		`{"errors":false,"items":[{"index":{"_index":"documents","_id":"one","status":400,"error":{"reason":"mapping"}}}]}`,
		`{"errors":true,"items":[{"index":{"_index":"documents","_id":"one","status":201}}]}`,
	} {
		var response bulkResponse
		if err := jsonv2.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		if err := response.validate(bulkOperationIndex, "documents", []string{"one"}, false); err == nil {
			t.Fatalf("invalid acknowledgment accepted: %s", raw)
		}
	}
	var missing bulkResponse
	if err := jsonv2.Unmarshal([]byte(`{"errors":false,"items":[{"delete":{"_index":"documents","_id":"one","status":404}}]}`), &missing); err != nil {
		t.Fatal(err)
	}
	if err := missing.validate(bulkOperationDelete, "documents", []string{"one"}, true); err != nil {
		t.Fatal(err)
	}
	if err := missing.validate(bulkOperationDelete, "documents", []string{"one"}, false); err == nil {
		t.Fatal("conditional deletion accepted a vanished snapshot member")
	}
}

type failingBody struct {
	io.Reader
	cause  error
	closed bool
}

func (f *failingBody) Close() error { f.closed = true; return f.cause }

type bodyClient struct{ body *failingBody }

func (b bodyClient) Perform(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: b.body}, nil
}

func TestNativeBodyOwnershipAndBounds(t *testing.T) {
	cause := errors.New("close failed")
	body := &failingBody{Reader: strings.NewReader(`{}`), cause: cause}
	store := &Store{client: bodyClient{body: body}, maxResponseBytes: 16}
	if _, err := store.request(t.Context(), http.MethodGet, "/documents/_mapping", nil, nil); !errors.Is(err, cause) || !body.closed {
		t.Fatalf("close failure disappeared: %v", err)
	}
	body = &failingBody{Reader: strings.NewReader(strings.Repeat("x", 17))}
	store.client = bodyClient{body: body}
	if raw, err := store.request(t.Context(), http.MethodGet, "/documents/_mapping", nil, nil); err == nil || len(raw) != 17 || !body.closed {
		t.Fatalf("response bound failed: %s, %v", raw, err)
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

func TestWriteFailureRetainsOnlyAttributableItemAcknowledgments(t *testing.T) {
	ids := []string{"accepted", "denied", "unknown"}
	resp := bulkResponse{Errors: new(true), Items: []map[bulkOperation]bulkItemResult{
		{bulkOperationIndex: {ID: "accepted", Index: "docs", Status: new(201)}},
		{bulkOperationIndex: {ID: "denied", Index: "docs", Status: new(409), Error: []byte(`{"type":"conflict"}`)}},
	}}
	cause := errors.New("partial bulk response")
	w := resp.writeFailure("docs", ids, cause)
	if !errors.Is(w, cause) || !slices.Equal(w.Outcome().Acknowledged, ids[:1]) || !slices.Equal(w.Outcome().Rejected, ids[1:2]) || !slices.Equal(w.Outcome().Unconfirmed, ids[2:]) {
		t.Fatalf("incorrect write evidence: %+v", *w)
	}
	resp.Errors = new(false)
	w = resp.writeFailure("docs", ids, cause)
	if len(w.Outcome().Acknowledged) != 0 || len(w.Outcome().Rejected) != 0 || !slices.Equal(w.Outcome().Unconfirmed, ids) {
		t.Fatalf("contradictory bulk reply invented success: %+v", *w)
	}
}
