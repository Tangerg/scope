package opensearch

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type scrollCleanupClient struct {
	pages        []string
	clears       []string
	cleanupError error
}

func (s *scrollCleanupClient) Stream(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodDelete && request.URL.Path == "/_search/scroll" {
		var payload struct {
			IDs []string `json:"scroll_id"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &payload); err != nil {
			return nil, err
		}
		s.clears = append(s.clears, payload.IDs...)
		if s.cleanupError != nil {
			return nil, s.cleanupError
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"succeeded":true,"num_freed":1}`))}, nil
	}
	if len(s.pages) == 0 {
		return nil, errors.New("unexpected scroll request")
	}
	page := s.pages[0]
	s.pages = s.pages[1:]
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(page))}, nil
}

func TestOversizedScrollClearsLatestObservedCursor(t *testing.T) {
	const oversized = `{"_scroll_id":"latest","hits":{"padding":"`
	for _, later := range []bool{false, true} {
		for _, nativeLimit := range []bool{false, true} {
			t.Run(fmtCase(later, nativeLimit), func(t *testing.T) {
				fixture := newProtocolFixture(t)
				store := fixture.store()
				if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
					t.Fatal(err)
				}
				client := &scrollCleanupClient{}
				budget := vectorstore.ScanBudget{MaxRecords: 10, MaxBytes: 256, Timeout: time.Second}
				if later {
					raw, err := jsonv2.Marshal(fixture.page([]searchHit{fixture.hit("one")}, 2, "previous"))
					if err != nil {
						t.Fatal(err)
					}
					client.pages = append(client.pages, string(raw))
					budget.MaxBytes += int64(len(raw))
				}
				if nativeLimit {
					store.maxResponseBytes = budget.MaxBytes
					budget.MaxBytes *= 2
				}
				client.pages = append(client.pages, oversized+strings.Repeat("x", 8192)+`"}}`)
				store.client = client
				result, err := store.selectMatches(context.Background(), filter.IsNull("absent"), budget)
				if err == nil || result != nil {
					t.Fatalf("oversized scroll succeeded: %v, %v", result, err)
				}
				if !nativeLimit && !errors.Is(err, vectorstore.ErrScanBudget) {
					t.Fatalf("error = %v", err)
				}
				if !slices.Equal(client.clears, []string{"latest"}) {
					t.Fatalf("cleared cursors = %v", client.clears)
				}
			})
		}
	}
}

func fmtCase(later, nativeLimit bool) string {
	page, limit := "first", "scan_budget"
	if later {
		page = "later"
	}
	if nativeLimit {
		limit = "response_limit"
	}
	return page + "/" + limit
}

func TestOversizedScrollPreservesCleanupFailure(t *testing.T) {
	cause := errors.New("clear failed")
	client := &scrollCleanupClient{pages: []string{`{"_scroll_id":"known","padding":"` + strings.Repeat("x", 512) + `"}`}, cleanupError: cause}
	store := &Store{client: client, indexName: "documents", maxResponseBytes: 1024}
	result, err := store.selectMatches(t.Context(), filter.IsNull("absent"), vectorstore.ScanBudget{MaxRecords: 1, MaxBytes: 64, Timeout: time.Second})
	if result != nil || !errors.Is(err, vectorstore.ErrScanBudget) || !errors.Is(err, cause) {
		t.Fatalf("result=%v error=%v", result, err)
	}
	if !slices.Equal(client.clears, []string{"known"}) {
		t.Fatalf("cleared cursors=%v", client.clears)
	}
}
