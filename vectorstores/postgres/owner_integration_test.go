//go:build integration

package postgres_test

import (
	"encoding/json"
	"math"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestLiveCurrentOwners(t *testing.T) {
	for _, backend := range []string{"pgvector", "cockroachdb"} {
		t.Run(backend, func(t *testing.T) {
			t.Run("exact_metadata_and_binary_identity", func(t *testing.T) {
				fixture := newNativeFixture(t, backend, "")
				facts := metadata.Map{"$key": json.RawMessage(`{"exact":9007199254740993,"scale":1e3,"huge":1e1000000,"nul":"\u0000","zero":{}}`), "scope_metadata": json.RawMessage(`"business"`)}
				docs := []*document.Document{{ID: "null", Text: "text"}, {ID: "empty", Text: "text", Metadata: metadata.Map{}}, {ID: "bytes\x00/\"\\文", Text: "text", Metadata: facts}, {ID: "CASE", Text: "text"}, {ID: "case", Text: "text"}}
				if err := fixture.install(t.Context(), docs); err != nil {
					t.Fatal(err)
				}
				response, err := fixture.store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{TopK: len(docs)}})
				if err != nil || len(response.Results) != len(docs) {
					t.Fatalf("native binary results: response=%v error=%v", response, err)
				}
				for _, doc := range docs {
					index := slices.IndexFunc(response.Results, func(hit *vectorstore.SearchResult) bool { return hit.Document.ID == doc.ID })
					if index < 0 {
						t.Fatalf("missing binary ID %q", doc.ID)
					}
					got := response.Results[index].Document.Metadata
					if (got == nil) != (doc.Metadata == nil) || !got.Equal(doc.Metadata) {
						t.Fatalf("Core encoding lost: got=%v want=%v", got, doc.Metadata)
					}
				}
				if err = fixture.store.DeleteIDs(t.Context(), []string{docs[2].ID, docs[2].ID, "unknown"}); err != nil {
					t.Fatal(err)
				}
				remaining, err := fixture.ids(t.Context())
				if err != nil || len(remaining) != len(docs)-1 || slices.Contains(remaining, docs[2].ID) {
					t.Fatalf("binary deletion: ids=%v error=%v", remaining, err)
				}
			})
			t.Run("invalid_low_score_identity", func(t *testing.T) {
				fixture := newNativeFixture(t, backend, "")
				if _, err := fixture.pool.Exec(t.Context(), "INSERT INTO "+fixture.table+"(id,content,facts,embedding) VALUES($1,'text',$2,'[-1,0]'::vector)", []byte{}, []byte("{}")); err != nil {
					t.Fatal(err)
				}
				response, err := fixture.store.Search(t.Context(), &vectorstore.SearchRequest{Query: "text", Options: vectorstore.SearchOptions{MinScore: 0.5}})
				if err == nil || response != nil {
					t.Fatalf("corrupt low-score identity hidden: response=%v error=%v", response, err)
				}
			})
			t.Run("late_vector_failure_is_atomic", func(t *testing.T) {
				fixture := newNativeFixture(t, backend, "")
				fixture.vectorFor = func(text string) []float64 {
					if text == "late" {
						return []float64{math.MaxFloat64, 0}
					}
					return []float64{1, 0}
				}
				docs := []*document.Document{{ID: "first", Text: "first"}, {ID: "late", Text: "late"}}
				if err := fixture.store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err == nil {
					t.Fatal("unrepresentable late vector accepted")
				}
				ids, err := fixture.ids(t.Context())
				if err != nil || len(ids) != 0 {
					t.Fatalf("late vector wrote prefix: ids=%v error=%v", ids, err)
				}
			})
			t.Run("native_failure_rolls_back_upsert_prefix", func(t *testing.T) {
				fixture := newNativeFixture(t, backend, "")
				if _, err := fixture.pool.Exec(t.Context(), "ALTER TABLE "+fixture.table+" ADD CONSTRAINT scope_failure CHECK(content <> 'forbidden')"); err != nil {
					t.Fatal(err)
				}
				docs := []*document.Document{{ID: "first", Text: "first"}, {ID: "late", Text: "forbidden"}}
				if err := fixture.store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err == nil {
					t.Fatal("native failure hidden")
				}
				ids, err := fixture.ids(t.Context())
				if err != nil || len(ids) != 0 || fixture.pool.Stat().AcquiredConns() != 0 {
					t.Fatalf("native failure left prefix or lease: ids=%v error=%v", ids, err)
				}
			})
			for _, metric := range []string{"cosine", "l2", "ip"} {
				t.Run("raw_distance/"+metric, func(t *testing.T) {
					fixture := newNativeFixture(t, backend, metric)
					fixture.vectorFor = func(text string) []float64 {
						if metric == "ip" {
							switch text {
							case "winner":
								return []float64{3, 0}
							case "runner":
								return []float64{2, 0}
							}
						}
						if text == "runner" {
							return []float64{0, 1}
						}
						return []float64{1, 0}
					}
					docs := []*document.Document{{ID: "a-runner", Text: "runner"}, {ID: "z-winner", Text: "winner"}}
					if err := fixture.install(t.Context(), docs); err != nil {
						t.Fatal(err)
					}
					response, err := fixture.store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1}})
					if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "z-winner" {
						t.Fatalf("%s native rank: response=%v error=%v", metric, response, err)
					}
				})
			}
			t.Run("corrupt_source_outside_predicate", func(t *testing.T) {
				fixture := newNativeFixture(t, backend, "")
				if _, err := fixture.pool.Exec(t.Context(), "INSERT INTO "+fixture.table+"(id,content,facts,embedding) VALUES($1,'text',$2,'[1,0]'::vector)", []byte("bad"), []byte("[]")); err != nil {
					t.Fatal(err)
				}
				predicate := nativePredicate(t, `absent == 'never'`)
				if _, err := fixture.search(t.Context(), predicate, 1); err == nil {
					t.Fatal("bad source hidden by predicate")
				}
				if err := fixture.store.DeleteWhere(t.Context(), predicate); err == nil {
					t.Fatal("bad source ignored during deletion")
				}
				if err := fixture.store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "new", Text: "text"}}}); err == nil {
					t.Fatal("bad source ignored during upsert")
				}
				ids, err := fixture.ids(t.Context())
				if err != nil || !slices.Equal(ids, []string{"bad"}) {
					t.Fatalf("bad source caused effects: ids=%v error=%v", ids, err)
				}
			})
			t.Run("current_schema_rejects_JSONB", func(t *testing.T) {
				fixture := newNativeFixture(t, backend, "")
				if _, err := fixture.pool.Exec(t.Context(), "ALTER TABLE "+fixture.table+" DROP COLUMN facts"); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.pool.Exec(t.Context(), "ALTER TABLE "+fixture.table+" ADD COLUMN facts JSONB NOT NULL"); err != nil {
					t.Fatal(err)
				}
				if err := fixture.store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text"}}}); err == nil {
					t.Fatal("superseded JSONB schema accepted")
				}
			})
			t.Run("large_array_index_and_NUL_filter", func(t *testing.T) {
				fixture := newNativeFixture(t, backend, "")
				facts, err := metadata.FromValues(map[string]any{"value": "\x00", "root": map[string]any{"nul\x00key": "match"}})
				if err != nil {
					t.Fatal(err)
				}
				docs := []*document.Document{{ID: "one", Text: "text", Metadata: facts}}
				if err := fixture.install(t.Context(), docs); err != nil {
					t.Fatal(err)
				}
				for _, predicate := range []filter.Predicate{
					nativePredicate(t, `value[2147483648] is null`),
					filter.EQ("value", "\x00"),
					filter.EQ(filter.Index("root", "nul\x00key"), "match"),
				} {
					ids, queryErr := fixture.search(t.Context(), predicate, 1)
					if queryErr != nil || !slices.Equal(ids, []string{"one"}) {
						t.Fatalf("Core filter %s: ids=%v error=%v", predicate, ids, queryErr)
					}
				}
			})
		})
	}
}
