package vectara

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// corpusServer answers document listings from scripted pages and records every
// deletion it receives.
type corpusServer struct {
	pages   []string
	listed  int
	deleted []string
	methods []string
}

func (c *corpusServer) start(t *testing.T) *Store {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		c.methods = append(c.methods, request.Method)
		if request.Method == http.MethodDelete {
			c.deleted = append(c.deleted, request.URL.Path[strings.LastIndexByte(request.URL.Path, '/')+1:])
			_, _ = writer.Write([]byte(`{}`))
			return
		}
		if c.listed >= len(c.pages) {
			t.Errorf("unexpected list request %d", c.listed+1)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		page := c.pages[c.listed]
		c.listed++
		_, _ = writer.Write([]byte(page))
	}))
	t.Cleanup(server.Close)

	return &Store{
		endpoint:         server.URL,
		httpClient:       server.Client(),
		corpusKey:        "corpus",
		maxResponseBytes: 1 << 20,
		metadataPrefix:   "doc",
	}
}

func deleteWhereTenant(t *testing.T, store *Store) error {
	t.Helper()
	expression, err := filter.Parse(`tenant == 'acme'`)
	if err != nil {
		t.Fatal(err)
	}
	return store.DeleteWhere(t.Context(), expression)
}

// A page key is the only exhaustion signal Vectara documents, so an empty page
// that still carries one must not end the walk.
func TestDeleteWhereFollowsPageKeyThroughEmptyPage(t *testing.T) {
	t.Parallel()

	server := &corpusServer{pages: []string{
		`{"documents":[{"id":"one"}],"metadata":{"page_key":"k1"}}`,
		`{"documents":[],"metadata":{"page_key":"k2"}}`,
		`{"documents":[{"id":"two"}],"metadata":{}}`,
	}}
	store := server.start(t)
	if err := deleteWhereTenant(t, store); err != nil {
		t.Fatalf("DeleteWhere() = %v, want nil", err)
	}
	if server.listed != 3 {
		t.Fatalf("list requests = %d, want 3", server.listed)
	}
	if got := strings.Join(server.deleted, ","); got != "one,two" {
		t.Fatalf("deleted = %v, want one,two", server.deleted)
	}
}

// The page key belongs to the listing that produced it, so the whole matching
// set is enumerated before the first deletion changes the corpus.
func TestDeleteWhereEnumeratesBeforeDeleting(t *testing.T) {
	t.Parallel()

	server := &corpusServer{pages: []string{
		`{"documents":[{"id":"one"}],"metadata":{"page_key":"k1"}}`,
		`{"documents":[{"id":"two"}],"metadata":{}}`,
	}}
	if err := deleteWhereTenant(t, server.start(t)); err != nil {
		t.Fatalf("DeleteWhere() = %v, want nil", err)
	}
	if got := strings.Join(server.methods, ","); got != "GET,GET,DELETE,DELETE" {
		t.Fatalf("request order = %v, want every listing before every deletion", server.methods)
	}
}

// A listing that names no document cannot be deleted by id.
func TestDeleteWhereRejectsDocumentWithoutID(t *testing.T) {
	t.Parallel()

	server := &corpusServer{pages: []string{`{"documents":[{"id":""}],"metadata":{}}`}}
	err := deleteWhereTenant(t, server.start(t))
	if err == nil || !strings.Contains(err.Error(), "has no id") {
		t.Fatalf("DeleteWhere() = %v, want a missing-id error", err)
	}
	if len(server.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", server.deleted)
	}
}

// An empty filter must not become a corpus-wide deletion.
func TestDeleteWhereRefusesEmptyFilter(t *testing.T) {
	t.Parallel()

	server := &corpusServer{}
	if err := server.start(t).DeleteWhere(t.Context(), nil); err == nil {
		t.Fatal("DeleteWhere(nil) = nil, want an error")
	}
	if server.listed != 0 {
		t.Fatalf("list requests = %d, want none", server.listed)
	}
}
