package redis

import (
	"context"
	"errors"
	"strings"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// searchPages answers each FT.SEARCH with the next scripted page and records
// every key handed to DEL. The embedded interface stays nil because DeleteWhere
// must not reach any other command.
type searchPages struct {
	goredis.UniversalClient
	pages   []goredis.FTSearchResult
	calls   int
	deleted []string
}

func (s *searchPages) FTSearchWithArgs(
	ctx context.Context,
	index, query string,
	options *goredis.FTSearchOptions,
) *goredis.FTSearchCmd {
	command := new(goredis.FTSearchCmd)
	if s.calls < len(s.pages) {
		command.SetVal(s.pages[s.calls])
	}
	s.calls++
	return command
}

func (s *searchPages) Del(ctx context.Context, keys ...string) *goredis.IntCmd {
	s.deleted = append(s.deleted, keys...)
	command := goredis.NewIntCmd(ctx, "del")
	command.SetVal(int64(len(keys)))
	return command
}

func page(warnings []string, ids ...string) goredis.FTSearchResult {
	docs := make([]goredis.Document, len(ids))
	for index, id := range ids {
		docs[index] = goredis.Document{ID: id}
	}
	return goredis.FTSearchResult{Total: len(docs), Docs: docs, Warnings: warnings}
}

func deleteWhere(t *testing.T, client *searchPages) error {
	t.Helper()
	predicate, err := filter.Parse(`tenant == 'scope'`)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{
		client:     client,
		indexName:  "documents",
		fieldTypes: map[string]MetadataFieldType{"tenant": FieldTag},
	}
	return store.DeleteWhere(t.Context(), predicate)
}

// A page shorter than the requested limit does not establish that the match set
// is exhausted, because RediSearch truncates a timed-out scan and still answers
// successfully.
func TestDeleteWhereKeepsQueryingAfterAShortPage(t *testing.T) {
	client := &searchPages{pages: []goredis.FTSearchResult{
		page(nil, "a", "b"),
		page(nil, "c"),
		page(nil),
	}}
	if err := deleteWhere(t, client); err != nil {
		t.Fatalf("DeleteWhere() = %v, want nil", err)
	}
	if got := strings.Join(client.deleted, ","); got != "a,b,c" {
		t.Fatalf("deleted = %q, want \"a,b,c\"", got)
	}
	if client.calls != 3 {
		t.Fatalf("FT.SEARCH calls = %d, want 3", client.calls)
	}
}

func TestDeleteWhereRejectsPartialSearchResult(t *testing.T) {
	client := &searchPages{pages: []goredis.FTSearchResult{
		page([]string{"Timeout limit was reached"}, "a"),
	}}
	err := deleteWhere(t, client)
	if err == nil || !strings.Contains(err.Error(), "returned a partial result: Timeout limit was reached") {
		t.Fatalf("DeleteWhere() = %v, want a partial-result error", err)
	}
	if len(client.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", client.deleted)
	}
}

func TestSearchCompletenessRejectsUnreadableHit(t *testing.T) {
	result := goredis.FTSearchResult{Docs: []goredis.Document{
		{ID: "a"},
		{ID: "b", Error: errors.New("document is gone")},
	}}
	err := checkSearchCompleteness("documents", result)
	if err == nil || !strings.Contains(err.Error(), `hit "b": document is gone`) {
		t.Fatalf("checkSearchCompleteness() = %v, want a hit error", err)
	}
}
