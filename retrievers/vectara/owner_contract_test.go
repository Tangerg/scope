package vectara

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/rag"
)

type requestLog struct{ Method, Path, Body string }

func fixture(t *testing.T, ids []string, operation func(http.ResponseWriter, *http.Request, string)) (*Corpus, *[]requestLog) {
	t.Helper()
	var calls []requestLog
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "secret" {
			t.Error("missing auth")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		calls = append(calls, requestLog{r.Method, r.URL.RequestURI(), string(body)})
		if r.Method == "GET" && r.URL.Path == "/v2/corpora/manuals" {
			fmt.Fprint(w, `{"key":"manuals","enabled":true}`)
			return
		}
		operation(w, r, string(body))
	}))
	t.Cleanup(server.Close)
	corpus, err := New(t.Context(), Config{Endpoint: server.URL, CorpusKey: "manuals", APIKey: "secret", AllowedDocumentIDs: ids, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return corpus, &calls
}

func TestBoundRetrievalUsesOneNativeRequestAndKeepsSiblingPassages(t *testing.T) {
	ids := []string{"brochure's id"}
	const want = "doc.id IN ('brochure''s id')"
	corpus, calls := fixture(t, ids, func(w http.ResponseWriter, r *http.Request, body string) {
		if r.Method != "POST" || r.URL.Path != "/v2/corpora/manuals/query" {
			t.Fatalf("unexpected path %s", r.URL)
		}
		var q struct {
			Query  string `json:"query"`
			Search struct {
				Filter string `json:"metadata_filter"`
			} `json:"search"`
			Generation json.RawMessage `json:"generation"`
		}
		if err := jsonv2.Unmarshal([]byte(body), &q); err != nil || q.Query != "orchid" || q.Search.Filter != want || q.Generation != nil {
			t.Fatalf("native request %s %v", body, err)
		}
		fmt.Fprint(w, `{"search_results":[{"result_type":"text","document_id":"brochure's id","text":"leaf","score":-2,"document_metadata":{"n":9007199254740993},"part_metadata":{"page":1}},{"result_type":"text","document_id":"brochure's id","text":"root","score":3,"part_metadata":{"page":2}}]}`)
	})
	ids[0] = "outside"
	q, _ := rag.NewQuery("orchid")
	results, err := corpus.Retrieve(t.Context(), q)
	if err != nil || len(results) != 2 || len(*calls) != 2 {
		t.Fatalf("retrieval: %v %v %#v", results, err, *calls)
	}
	if results[0].Score != -2 || results[1].Score != 3 || results[0].Document.ID == results[1].Document.ID || string(results[0].Document.Metadata["n"]) != "9007199254740993" || string(results[0].Document.Metadata[PartMetadataKey]) != `{"page":1}` {
		t.Fatalf("passage order, identity or facts lost: %v", results)
	}
}

func TestEmptyScopeAndInvalidInputsNeverReachNativeOperations(t *testing.T) {
	corpus, calls := fixture(t, []string{}, func(http.ResponseWriter, *http.Request, string) { t.Error("unexpected native operation") })
	q, _ := rag.NewQuery("query")
	hits, err := corpus.Retrieve(t.Context(), q)
	if err != nil || len(hits) != 0 {
		t.Fatal(hits, err)
	}
	if err = corpus.CreateDocument(t.Context(), &document.Document{ID: "outside", Text: "x"}); err == nil {
		t.Fatal("unauthorized create")
	}
	if err = corpus.DeleteDocument(t.Context(), "outside"); err == nil {
		t.Fatal("unauthorized delete")
	}
	if _, err = corpus.Retrieve(t.Context(), rag.Query{}); err == nil {
		t.Fatal("invalid query")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = corpus.Retrieve(ctx, q); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatal(*calls)
	}
	for _, ids := range [][]string{{"   "}, {strings.Repeat("q", 8000)}} {
		if _, err = bindScope(ids); err == nil {
			t.Fatal("invalid scope")
		}
	}
}

func TestScopeAndMalformedNativeEvidenceFailClosed(t *testing.T) {
	for _, hit := range []string{
		`{"document_id":"outside","result_type":"text","score":1,"text":"x"}`,
		`{"document_id":"one","corpus_key":"foreign","result_type":"text","score":1,"text":"x"}`,
		`{"document_id":"one","request_corpora_index":1,"result_type":"text","score":1,"text":"x"}`,
		`{"document_id":"one","result_type":"text","text":"x"}`,
		`{"document_id":"one","result_type":"image","score":1,"text":"x"}`,
		`{"document_id":"one","result_type":"text","score":1,"text":"x","document_metadata":{"vectara_document_id":"forged"}}`,
	} {
		c, _ := fixture(t, []string{"one"}, func(w http.ResponseWriter, _ *http.Request, _ string) { fmt.Fprintf(w, `{"search_results":[%s]}`, hit) })
		q, _ := rag.NewQuery("q")
		hits, err := c.Retrieve(t.Context(), q)
		if err == nil || hits != nil {
			t.Fatalf("accepted %s", hit)
		}
	}
}

func TestCreateIsAcknowledgedNeverDeletesToReplace(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		data         string
		ok, conflict bool
	}{
		{"created", 201, `{"id":"one"}`, true, false}, {"exists", 409, `{}`, false, true}, {"wrong ack", 201, `{"id":"two"}`, false, false}, {"broken ack", 201, `{`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := fixture(t, nil, func(w http.ResponseWriter, r *http.Request, body string) {
				if r.Method != "POST" || r.URL.RequestURI() != "/v2/corpora/manuals/documents?wait_for=searchable" {
					t.Fatalf("destructive or unexpected request %s %s", r.Method, r.URL)
				}
				var in struct {
					Type     string       `json:"type"`
					ID       string       `json:"id"`
					Metadata metadata.Map `json:"metadata"`
					Parts    []struct {
						Text string `json:"text"`
					} `json:"document_parts"`
				}
				if err := jsonv2.Unmarshal([]byte(body), &in); err != nil || in.ID != "one" || in.Type != "core" || len(in.Parts) != 1 || in.Parts[0].Text != "hello" || string(in.Metadata["n"]) != "9007199254740993" {
					t.Fatalf("native write lost data: %s", body)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.data)
			})
			facts := make(metadata.Map)
			if err := facts.Set("n", int64(9007199254740993)); err != nil {
				t.Fatal(err)
			}
			err := c.CreateDocument(t.Context(), &document.Document{ID: "one", Text: "hello", Metadata: facts})
			if (err == nil) != tc.ok || errors.Is(err, ErrDocumentExists) != tc.conflict || len(*calls) != 2 {
				t.Fatalf("outcome %v calls %v", err, *calls)
			}
		})
	}
}

func TestDeleteAddressesOnlyOneEscapedIdentity(t *testing.T) {
	var paths []string
	c, _ := fixture(t, []string{"..", "a/b"}, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Method != "DELETE" {
			t.Error("unexpected mutation")
		}
		paths = append(paths, r.URL.EscapedPath())
		w.WriteHeader(404)
	})
	for _, id := range []string{"..", "a/b"} {
		if err := c.DeleteDocument(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(paths, []string{"/v2/corpora/manuals/documents/%2E%2E", "/v2/corpora/manuals/documents/a%2Fb"}) {
		t.Fatal(paths)
	}
}

func TestHostedSegmentationPreservesRevisionWithoutInventingByteRanges(t *testing.T) {
	stored := make(chan json.RawMessage, 1)
	corpus, calls := fixture(t, []string{"article"}, func(w http.ResponseWriter, r *http.Request, body string) {
		switch r.URL.Path {
		case "/v2/corpora/manuals/documents":
			var in struct {
				Metadata json.RawMessage `json:"metadata"`
			}
			if err := jsonv2.Unmarshal([]byte(body), &in); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			stored <- in.Metadata
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"article"}`)
		case "/v2/corpora/manuals/query":
			fmt.Fprintf(w, `{"search_results":[{"result_type":"text","document_id":"article","text":"excerpt","score":1,"document_metadata":%s}]}`, <-stored)
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	p := document.NewProvenance("urn:book:guide", []byte("entire book"))
	p.Page = 4
	p.Bytes = &document.ByteRange{Start: 81, End: 148}
	doc := &document.Document{ID: "article", Text: "the submitted excerpt has more words", Provenance: p}
	if err := corpus.CreateDocument(t.Context(), doc); err != nil {
		t.Fatal(err)
	}
	q, _ := rag.NewQuery("excerpt")
	hits, retrieveErr := corpus.Retrieve(t.Context(), q)
	if retrieveErr != nil || len(hits) != 1 {
		t.Fatal(hits, retrieveErr)
	}
	got := hits[0].Document.Provenance
	if got == nil || got.Source != p.Source || got.Revision != p.Revision || got.Page != 4 || got.Bytes != nil || p.Bytes == nil {
		t.Fatalf("unfaithful passage provenance: %+v", got)
	}
	if _, leaked := hits[0].Document.Metadata[document.StoredProvenanceKey]; leaked {
		t.Fatal("physical evidence key exposed as a business field")
	}
	before := len(*calls)
	doc.Metadata = metadata.Map{}
	if err := doc.Metadata.Set(document.StoredProvenanceKey, "spoof"); err != nil {
		t.Fatal(err)
	}
	if err := corpus.CreateDocument(t.Context(), doc); err == nil || len(*calls) != before {
		t.Fatal("a reserved business key reached the hosted service")
	}
}

func TestCreateDistinguishesMissingAndEmptyBusinessMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta metadata.Map
		want string
	}{{"absent", nil, ""}, {"empty", metadata.Map{}, "{}"}} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := fixture(t, nil, func(w http.ResponseWriter, r *http.Request, body string) {
				var in struct {
					Metadata json.RawMessage `json:"metadata"`
				}
				if err := jsonv2.Unmarshal([]byte(body), &in); err != nil || string(in.Metadata) != tc.want {
					t.Errorf("metadata = %s, want %s: %v", in.Metadata, tc.want, err)
				}
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"id":"document"}`)
			})
			if err := c.CreateDocument(t.Context(), &document.Document{ID: "document", Text: "text", Metadata: tc.meta}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
