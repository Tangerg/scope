package inmemory_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestStoreNumericFiltersPreserveMixedNumberValues(t *testing.T) {
	for _, test := range []struct {
		name      string
		stored    any
		predicate filter.Predicate
		matches   bool
	}{
		{
			name: "integer above float is not equal", stored: int64(9007199254740993),
			predicate: filter.EQ("value", float64(9007199254740992)),
		},
		{
			name: "integer above float orders greater", stored: int64(9007199254740993),
			predicate: filter.GT("value", float64(9007199254740992)), matches: true,
		},
		{
			name: "float below integer is not equal", stored: json.Number("9007199254740992.0"),
			predicate: filter.EQ("value", int64(9007199254740993)),
		},
		{
			name: "float below integer orders less", stored: json.Number("9007199254740992.0"),
			predicate: filter.LT("value", int64(9007199254740993)), matches: true,
		},
		{
			name: "negative integer above float", stored: int64(math.MinInt64 + 1),
			predicate: filter.GT("value", float64(math.MinInt64)), matches: true,
		},
		{
			name: "unsigned integer below float", stored: uint64(math.MaxUint64),
			predicate: filter.LT("value", float64(1<<64)), matches: true,
		},
		{
			name: "membership preserves integer", stored: int64(9007199254740993),
			predicate: filter.In("value", []float64{9007199254740992}),
		},
		{
			name: "collection membership preserves integer", stored: []int64{9007199254740993},
			predicate: filter.Has("value", float64(9007199254740992)),
		},
		{
			name: "JSON integer beyond uint64", stored: json.Number("18446744073709551616"),
			predicate: filter.EQ("value", float64(1<<64)), matches: true,
		},
		{
			name: "JSON integer below int64", stored: json.Number("-9223372036854775809"),
			predicate: filter.LT("value", int64(math.MinInt64)), matches: true,
		},
		{
			name: "equal values across number kinds", stored: int64(9007199254740992),
			predicate: filter.EQ("value", float64(9007199254740992)), matches: true,
		},
		{
			name: "negative fractional boundary", stored: int64(-1),
			predicate: filter.LT("value", -0.5), matches: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newStore(t)
			doc := mustDoc(t, "record", "record text", map[string]any{"value": test.stored})
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{doc}}); err != nil {
				t.Fatal(err)
			}
			results, err := search(store, t.Context(), &vectorstore.SearchRequest{
				Query: "record text", Options: vectorstore.SearchOptions{Filter: test.predicate},
			})
			if err != nil {
				t.Errorf("Search() error = %v", err)
			} else if test.matches {
				if len(results) != 1 || results[0].Document.ID != doc.ID {
					t.Errorf("Search() = %v, want record %q", results, doc.ID)
				}
			} else if len(results) != 0 {
				t.Errorf("Search() returned %d records, want no match", len(results))
			}

			if err := store.DeleteWhere(t.Context(), test.predicate); err != nil {
				t.Errorf("DeleteWhere() error = %v", err)
			}
			remaining := 1
			if test.matches {
				remaining = 0
			}
			if got := store.Len(); got != remaining {
				t.Errorf("records after DeleteWhere() = %d, want %d", got, remaining)
			}
		})
	}
}
