package elastic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func scrollPage(cursor string, total int, hits ...string) string {
	return fmt.Sprintf(`{"_scroll_id":%q,"timed_out":false,"_shards":{"total":1,"successful":1,"failed":0},"hits":{"total":{"value":%d,"relation":"eq"},"hits":[%s]}}`, cursor, total, strings.Join(hits, ","))
}

func scrollHit(id, tenant string) string {
	return fmt.Sprintf(`{"_index":"documents","_id":%q,"_seq_no":3,"_primary_term":1,"_source":{"tenant":%q}}`, id, tenant)
}

func configuredScroll(t *testing.T, transport *scriptedTransport, configure func(*ScrollConfig)) *Scroll {
	t.Helper()
	config := ScrollConfig{Client: protocolClient(t, transport, 0), Index: "documents", Request: []byte(`{"query":{"match_all":{}}}`), Budget: fixtureBudget(), Decode: decodeFixtureHit}
	if configure != nil {
		configure(&config)
	}
	scroll, err := NewScroll(config)
	if err != nil {
		t.Fatal(err)
	}
	return scroll
}

func TestScrollExhaustsMembershipBeforeReturningAndClearsLatestCursor(t *testing.T) {
	transport := &scriptedTransport{t: t, replies: []wireReply{
		{status: 200, body: scrollPage("first", 2, scrollHit("one", "alpha"))},
		{status: 200, body: scrollPage("second", 2, scrollHit("two", "beta"))},
		{status: 200, body: scrollPage("last", 2)},
		{status: 200, body: `{"succeeded":true,"num_freed":1}`},
	}}
	scroll := configuredScroll(t, transport, nil)
	selected, err := scroll.Select(t.Context(), filter.EQ("tenant", "alpha"))
	if err != nil || len(selected) != 1 || selected["one"] != (Version{SeqNo: 3, PrimaryTerm: 1}) {
		t.Fatalf("selection=%v error=%v", selected, err)
	}
	for id, version := range selected {
		if err := version.Validate(); err != nil {
			t.Fatalf("invalid version for %q: %v", id, err)
		}
	}
	var paths []string
	for _, request := range transport.requests {
		paths = append(paths, request.path)
	}
	if !slices.Equal(paths, []string{"/documents/_search", "/_search/scroll", "/_search/scroll", "/_search/scroll"}) {
		t.Fatalf("paths=%v", paths)
	}
	cleanup := transport.requests[len(transport.requests)-1]
	if cleanup.method != http.MethodDelete || cleanup.body != `{"scroll_id":["last"]}` {
		t.Fatalf("cleanup=%+v", cleanup)
	}
	for _, body := range transport.bodies {
		if !body.closed {
			t.Fatal("response body leaked")
		}
	}
}

func TestScrollRejectsUntrustworthyPagesAndStillCleansUp(t *testing.T) {
	first := scrollPage("first", 1, scrollHit("one", "alpha"))
	for _, sample := range []struct {
		name   string
		pages  []string
		budget bool
	}{
		{name: "missing shard acknowledgment", pages: []string{strings.Replace(first, `"_shards"`, `"unrecognized_shards"`, 1)}},
		{name: "timeout", pages: []string{strings.Replace(first, `"timed_out":false`, `"timed_out":true`, 1)}},
		{name: "early termination", pages: []string{strings.Replace(first, `"timed_out":false`, `"timed_out":false,"terminated_early":true`, 1)}},
		{name: "inexact total", pages: []string{strings.Replace(first, `"relation":"eq"`, `"relation":"gte"`, 1)}},
		{name: "foreign index", pages: []string{strings.Replace(first, `"_index":"documents"`, `"_index":"other"`, 1)}},
		{name: "premature end", pages: []string{scrollPage("first", 1)}},
		{name: "changed total", pages: []string{first, scrollPage("second", 2)}},
		{name: "lost cursor", pages: []string{first, scrollPage("", 1)}},
		{name: "repeated identity", pages: []string{scrollPage("first", 2, scrollHit("one", "alpha"), scrollHit("one", "alpha"))}},
		{name: "more than total", pages: []string{scrollPage("first", 1, scrollHit("one", "alpha"), scrollHit("two", "alpha"))}},
		{name: "record budget", pages: []string{scrollPage("first", 11)}, budget: true},
		{name: "damaged source", pages: []string{strings.Replace(first, `"_source":{"tenant":"alpha"}`, `"_source":[]`, 1)}},
		{name: "truncated after cursor", pages: []string{`{"_scroll_id":"observed","hits":`}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			transport := &scriptedTransport{t: t}
			for _, page := range sample.pages {
				transport.replies = append(transport.replies, wireReply{status: 200, body: page})
			}
			transport.replies = append(transport.replies, wireReply{status: 200, body: `{"succeeded":true,"num_freed":1}`})
			selected, err := configuredScroll(t, transport, nil).Select(t.Context(), nil)
			if err == nil || selected != nil {
				t.Fatalf("selection=%v error=%v", selected, err)
			}
			if sample.budget && !errors.Is(err, vectorstore.ErrScanBudget) {
				t.Fatal(err)
			}
			last := transport.requests[len(transport.requests)-1]
			if last.method != http.MethodDelete {
				t.Fatalf("cursor leaked: %+v", transport.requests)
			}
		})
	}
}

func TestScrollResponseBudgetPreservesCleanupCause(t *testing.T) {
	cause := errors.New("cleanup failed")
	for _, nativeLimit := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeLimit), func(t *testing.T) {
			transport := &scriptedTransport{t: t, replies: []wireReply{
				{status: 200, body: `{"_scroll_id":"latest","padding":"` + strings.Repeat("x", 1024) + `"}`},
				{status: 503, body: `{}`, closeErr: cause},
			}}
			scroll := configuredScroll(t, transport, func(config *ScrollConfig) {
				if nativeLimit {
					config.Client = protocolClient(t, transport, 64)
				} else {
					config.Budget.MaxBytes = 64
				}
			})
			selected, err := scroll.Select(t.Context(), nil)
			if selected != nil || !errors.Is(err, cause) {
				t.Fatalf("selection=%v error=%v", selected, err)
			}
			want := vectorstore.ErrScanBudget
			if nativeLimit {
				want = ErrResponseLimit
			}
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
			if transport.requests[1].body != `{"scroll_id":["latest"]}` {
				t.Fatal("latest observed cursor was lost")
			}
		})
	}
}

func TestScrollCleanupMustAcknowledgeRelease(t *testing.T) {
	for _, receipt := range []string{`{`, `{}`, `{"succeeded":false,"num_freed":1}`, `{"succeeded":true,"num_freed":-1}`} {
		t.Run(receipt, func(t *testing.T) {
			transport := &scriptedTransport{t: t, replies: []wireReply{{status: 200, body: scrollPage("first", 0)}, {status: 200, body: receipt}}}
			selected, err := configuredScroll(t, transport, nil).Select(t.Context(), nil)
			if selected != nil || err == nil {
				t.Fatalf("selection=%v error=%v", selected, err)
			}
		})
	}
}

func TestScrollAdmissionAndDecoderBoundary(t *testing.T) {
	transport := &scriptedTransport{t: t}
	config := ScrollConfig{Client: protocolClient(t, transport, 0), Index: "documents", Request: []byte(`{}`), Budget: fixtureBudget(), Decode: decodeFixtureHit}
	for _, change := range []func(*ScrollConfig){
		func(c *ScrollConfig) { c.Client = nil },
		func(c *ScrollConfig) { c.Decode = nil },
		func(c *ScrollConfig) { c.Index = "*" },
		func(c *ScrollConfig) { c.Budget.MaxBytes = 0 },
		func(c *ScrollConfig) { c.Request = []byte(`{`) },
	} {
		invalid := config
		change(&invalid)
		if scroll, err := NewScroll(invalid); scroll != nil || err == nil {
			t.Fatalf("invalid config=%+v error=%v", invalid, err)
		}
	}
	for _, scroll := range []*Scroll{nil, {}} {
		if selected, err := scroll.Select(t.Context(), nil); selected != nil || err == nil {
			t.Fatalf("invalid scroll=%v error=%v", selected, err)
		}
	}
	scroll, err := NewScroll(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if selected, err := scroll.Select(ctx, nil); selected != nil || !errors.Is(err, context.Canceled) || len(transport.requests) != 0 {
		t.Fatalf("cancellation reached transport: %v %v", selected, err)
	}
	for _, doc := range []*document.Document{nil, {ID: "other", Text: "text"}, {ID: "one"}} {
		transport := &scriptedTransport{t: t, replies: []wireReply{{status: 200, body: scrollPage("first", 1, scrollHit("one", "alpha"))}, {status: 200, body: `{"succeeded":true,"num_freed":1}`}}}
		scroll := configuredScroll(t, transport, func(config *ScrollConfig) { config.Decode = func(Hit) (*document.Document, error) { return doc, nil } })
		if selected, err := scroll.Select(t.Context(), nil); selected != nil || err == nil {
			t.Fatalf("invalid decoded document=%v error=%v", selected, err)
		}
	}
}

func TestScrollTreatsTypedNilPredicateAsAbsentAndRejectsInvalidPredicatesBeforeIO(t *testing.T) {
	var predicate *filter.BinaryExpr
	transport := &scriptedTransport{t: t, replies: []wireReply{
		{status: 200, body: scrollPage("first", 0)},
		{status: 200, body: `{"succeeded":true,"num_freed":1}`},
	}}
	selected, err := configuredScroll(t, transport, nil).Select(t.Context(), predicate)
	if err != nil || len(selected) != 0 {
		t.Fatalf("typed nil: %v %v", selected, err)
	}
	transport = &scriptedTransport{t: t}
	selected, err = configuredScroll(t, transport, nil).Select(t.Context(), new(filter.BinaryExpr))
	if err == nil || selected != nil || len(transport.requests) != 0 {
		t.Fatalf("invalid predicate reached transport: %v %v", selected, err)
	}
}
