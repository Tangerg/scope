package vespa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestFilterConformance(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete=%t", deletion), func(t *testing.T) {
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				store, fixture := newFixtureStore(t, StoreConfig{})
				if err := store.Upsert(ctx, fixtureBatch(docs)); err != nil {
					return nil, err
				}
				if deletion {
					if err := store.DeleteMatching(ctx, predicate, fixtureBudget); err != nil {
						return nil, err
					}
					var deleted []string
					for _, doc := range docs {
						if _, exists := fixture.rows[doc.ID]; !exists {
							deleted = append(deleted, doc.ID)
						}
					}
					return deleted, nil
				}
				response, err := store.QueryByScan(ctx, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("filter conformance"), Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}}, fixtureBudget)
				if err != nil {
					return nil, err
				}
				var ids []string
				for _, hit := range response.Results {
					ids = append(ids, hit.Document.ID)
				}
				return ids, nil
			}})
		})
	}
}

func TestCanonicalMetadataAndNativeIdentityRoundTrip(t *testing.T) {
	store, fixture := newFixtureStore(t, StoreConfig{})
	facts := metadata.Map{"scope_namespace": json.RawMessage(`"business"`), "scope_documentid": json.RawMessage(`"metadata only"`), "scope_metadata_paths": json.RawMessage(`[]`), "documentid": json.RawMessage(`"business ID"`), "embedding": json.RawMessage(`{"content":"business"}`), "$key": json.RawMessage(`{"huge":1e1000,"exact":9007199254740993,"line":"f\no"}`)}
	input := []*document.Document{{ID: "null", Text: "text"}, {ID: "empty", Text: "text", Metadata: metadata.Map{}}, {ID: "quote/\"\\文", Text: "text", Metadata: facts}}
	if err := store.Upsert(t.Context(), fixtureBatch(input)); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixture.rows {
		if len(row) != 3 {
			t.Fatalf("competing native projections: %v", row)
		}
	}
	response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("text"), Options: vectorstore.SearchOptions{TopK: 3}}, fixtureBudget)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 3 {
		t.Fatalf("results=%v", response)
	}
	for _, doc := range input {
		index := slices.IndexFunc(response.Results, func(hit *vectorstore.SearchResult) bool { return hit.Document.ID == doc.ID })
		if index < 0 {
			t.Fatalf("lost ID %q", doc.ID)
		}
		got := response.Results[index].Document.Metadata
		if (got == nil) != (doc.Metadata == nil) || !got.Equal(doc.Metadata) {
			t.Fatalf("metadata %q: got=%v want=%v", doc.ID, got, doc.Metadata)
		}
	}
}

func TestNativeGuardRetainsChangedMetadata(t *testing.T) {
	store, fixture := newFixtureStore(t, StoreConfig{})
	facts, err := metadata.FromValues(map[string]any{"tenant": "old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text", Metadata: facts}})); err != nil {
		t.Fatal(err)
	}
	fixture.beforeDelete = func(_ string, row metadata.Map) {
		if err := row.Set(metadataField, `{"tenant":"new"}`); err != nil {
			t.Error(err)
		}
	}
	if err := store.DeleteMatching(t.Context(), filter.EQ("tenant", "old"), fixtureBudget); err != nil {
		t.Fatal(err)
	}
	if fixture.deletes != 1 || len(fixture.rows) != 1 {
		t.Fatalf("guard bypassed: deletes=%d rows=%d", fixture.deletes, len(fixture.rows))
	}
}

func TestIndexPreparesEveryVectorBeforeWriting(t *testing.T) {
	for name, late := range map[string][]float64{"overflow": {math.MaxFloat64, 0}, "underflow": {math.SmallestNonzeroFloat64, 0}, "width": {1}, "nan": {math.NaN(), 0}} {
		t.Run(name, func(t *testing.T) {
			store, fixture := newFixtureStore(t, StoreConfig{})
			batch := fixtureBatch([]*document.Document{{ID: "early", Text: "early"}, {ID: "late", Text: "late"}})
			batch.Records[1].Values = late
			err := store.Upsert(t.Context(), batch)
			if err == nil || fixture.writes != 0 {
				t.Fatalf("late failure wrote prefix: writes=%d error=%v", fixture.writes, err)
			}
		})
	}
}

func TestCurrentSourceValidatedBeforePredicateAndMutation(t *testing.T) {
	for _, operation := range []string{"search", "delete"} {
		for name, change := range map[string]func(metadata.Map){
			"missing metadata": func(row metadata.Map) { delete(row, metadataField) },
			"SQL-like null":    func(row metadata.Map) { row[metadataField] = json.RawMessage(`null`) },
			"invalid JSON":     func(row metadata.Map) { row[metadataField] = json.RawMessage(`"broken"`) },
			"metadata array":   func(row metadata.Map) { row[metadataField] = json.RawMessage(`"[]"`) },
			"old projection":   func(row metadata.Map) { row["value"] = json.RawMessage(`0`) },
			"wrong tensor": func(row metadata.Map) {
				row["embedding"] = json.RawMessage(`{"type":"tensor<double>(x[2])","values":[1,0]}`)
			},
			"null coordinate": func(row metadata.Map) {
				row["embedding"] = json.RawMessage(`{"type":"tensor<float>(x[2])","values":[1,null]}`)
			},
			"missing content": func(row metadata.Map) { delete(row, "content") },
		} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				store, fixture := newFixtureStore(t, StoreConfig{})
				docs := []*document.Document{{ID: "good", Text: "text"}, {ID: "z-bad", Text: "text"}}
				if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
					t.Fatal(err)
				}
				change(fixture.rows["z-bad"])
				writes := fixture.writes
				predicate := filter.EQ("absent", "never")
				var err error
				switch operation {
				case "search":
					_, err = store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("text"), Options: vectorstore.SearchOptions{Filter: predicate}}, fixtureBudget)
				case "delete":
					err = store.DeleteMatching(t.Context(), predicate, fixtureBudget)
				}
				if err == nil || fixture.writes != writes || fixture.deletes != 0 {
					t.Fatalf("corrupt source hidden: writes=%d deletes=%d error=%v", fixture.writes, fixture.deletes, err)
				}
			})
		}
	}
}

func TestSearchValidatesEveryHitBeforeMinimumScore(t *testing.T) {
	for name, change := range map[string]func(*queryHit){
		"foreign ID":       func(hit *queryHit) { hit.Fields[nativeIDField] = json.RawMessage(`"id:other:scope::one"`) },
		"missing ID":       func(hit *queryHit) { delete(hit.Fields, nativeIDField) },
		"missing metadata": func(hit *queryHit) { delete(hit.Fields, metadataField) },
		"invalid score":    func(hit *queryHit) { hit.Relevance = new(-1.0) },
		"missing score":    func(hit *queryHit) { hit.Relevance = nil },
	} {
		t.Run(name, func(t *testing.T) {
			store, fixture := newFixtureStore(t, StoreConfig{})
			if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
				t.Fatal(err)
			}
			fixture.searchResponse = func(_ []string, hits []queryHit) any {
				hits[0].Relevance = new(0.0)
				change(&hits[0])
				return map[string]any{"root": map[string]any{"coverage": queryCoverage{Coverage: 100, Full: true}, "children": hits}}
			}
			response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("text"), Options: vectorstore.SearchOptions{MinScore: 0.5}}, fixtureBudget)
			if err == nil || response != nil {
				t.Fatalf("low-score corruption hidden: response=%v error=%v", response, err)
			}
		})
	}
}

func TestOpaqueVisitContinuationAndFailures(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		t.Run(fmt.Sprintf("repeat=%t", repeated), func(t *testing.T) {
			store, fixture := newFixtureStore(t, StoreConfig{})
			calls := 0
			fixture.visitResponse = func(request *http.Request) any {
				calls++
				token := ""
				if calls == 1 || repeated {
					token = "opaque + / ? 文"
				}
				if calls == 2 && request.URL.Query().Get("continuation") != "opaque + / ? 文" {
					t.Error("continuation was rewritten")
				}
				return visitResponse{DocumentCount: new(0), Documents: []visitDocument{}, Continuation: token}
			}
			response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("text")}, fixtureBudget)
			if repeated {
				if err == nil || response != nil {
					t.Fatal("repeated token accepted")
				}
				return
			}
			if err != nil || response == nil || calls != 2 {
				t.Fatalf("empty continued page ended visit: calls=%d response=%v error=%v", calls, response, err)
			}
		})
	}
}

func TestNativeQueryCapIsAnErrorWithoutScopeLimitCopy(t *testing.T) {
	store, fixture := newFixtureStore(t, StoreConfig{})
	docs := make([]*document.Document, 401)
	for index := range docs {
		docs[index] = &document.Document{ID: fmt.Sprintf("%03d", index), Text: "text"}
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	response, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("text"), Options: vectorstore.SearchOptions{TopK: 401}}, fixtureBudget)
	if err != nil || len(response.Results) != 401 || fixture.queries != 4 {
		t.Fatalf("native groups: queries=%d response=%v error=%v", fixture.queries, response, err)
	}
	fixture.searchResponse = func(_ []string, hits []queryHit) any {
		return map[string]any{"root": map[string]any{"coverage": queryCoverage{Coverage: 100, Full: true}, "children": hits[:1]}}
	}
	response, err = store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("text")}, fixtureBudget)
	if err == nil || response != nil {
		t.Fatalf("native cap concealed: response=%v error=%v", response, err)
	}
}

func TestPredicateFailurePrecedesDeletion(t *testing.T) {
	store, fixture := newFixtureStore(t, StoreConfig{})
	var docs []*document.Document
	for index, value := range []any{0, "wrong type"} {
		facts, err := metadata.FromValues(map[string]any{"value": value})
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, &document.Document{ID: fmt.Sprint(index), Text: "text", Metadata: facts})
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	predicate, err := filter.Parse("value < 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMatching(t.Context(), predicate, fixtureBudget); err == nil || fixture.deletes != 0 {
		t.Fatalf("type error deleted prefix: deletes=%d error=%v", fixture.deletes, err)
	}
}

func TestPutRequiresNativeIdentityAcknowledgment(t *testing.T) {
	store, fixture := newFixtureStore(t, StoreConfig{})
	fixture.putResponse = func(string) any { return map[string]string{"id": "id:scope:scope::other"} }
	if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err == nil {
		t.Fatal("wrong native acknowledgment accepted")
	}
}

func TestNativeTextAndPhysicalFieldContracts(t *testing.T) {
	for _, bad := range []string{"\x00", "\x01", "\ufffe", "\ufdd0"} {
		store, fixture := newFixtureStore(t, StoreConfig{})
		err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one" + bad, Text: "text"}}))
		if err == nil || fixture.writes != 0 {
			t.Fatalf("non-native ID accepted: writes=%d error=%v", fixture.writes, err)
		}
	}
	for _, field := range []string{"scope_metadata", "scope_documentid", "documentid", "embedding"} {
		config := StoreConfig{Endpoint: "http://localhost:1", SchemaName: "scope", RankingProfile: "rank", ContentField: field, Space: fixtureSpace, Dimensions: 2}
		if _, err := NewStore(t.Context(), config); err == nil {
			t.Fatalf("conflicting physical field %q accepted", field)
		}
	}
	if err := validateText("a\t\n\r\x7f文"); err != nil {
		t.Fatal(err)
	}
	literal, err := quoteLiteral("quote\"\\\n文")
	if err != nil || strings.Contains(literal, "\\x") {
		t.Fatalf("non-JSON native literal: %q error=%v", literal, err)
	}
}

func TestVisitRejectsMalformedCountsAndIdentities(t *testing.T) {
	for name, response := range map[string]visitResponse{
		"missing count":  {Documents: []visitDocument{}},
		"negative count": {DocumentCount: new(-1)},
		"foreign ID":     {DocumentCount: new(1), Documents: []visitDocument{{ID: "id:other:scope::one"}}},
		"empty ID":       {DocumentCount: new(1), Documents: []visitDocument{{ID: "id:" + fixtureNamespace() + ":scope::"}}},
		"count mismatch": {DocumentCount: new(2), Documents: []visitDocument{}},
	} {
		t.Run(name, func(t *testing.T) {
			store, fixture := newFixtureStore(t, StoreConfig{})
			fixture.visitResponse = func(*http.Request) any { return response }
			if _, _, err := store.readSource(fixtureMeter(t, t.Context())); err == nil {
				t.Fatal("malformed visit accepted")
			}
		})
	}
	store, fixture := newFixtureStore(t, StoreConfig{})
	if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	fixture.visitResponse = func(*http.Request) any {
		return visitResponse{DocumentCount: new(2), Documents: []visitDocument{{ID: store.nativeID("one"), Fields: fixture.rows["one"]}, {ID: store.nativeID("one"), Fields: fixture.rows["one"]}}}
	}
	if _, _, err := store.readSource(fixtureMeter(t, t.Context())); err == nil {
		t.Fatal("duplicate visit IDs accepted")
	}
}

func TestConfigRejectsInvalidEndpointAndResponseBound(t *testing.T) {
	for _, endpoint := range []string{"", "relative", "file:///tmp/local", "http://host?query=1", "http://host?", "http://host/base?", "http://host#fragment", "http://host#", "http://host/base#", "http://user:password@host"} {
		config := StoreConfig{Endpoint: endpoint, SchemaName: "scope", RankingProfile: "rank", Space: fixtureSpace, Dimensions: 2}
		if err := config.Validate(); err == nil {
			t.Fatalf("invalid endpoint %q accepted", endpoint)
		}
	}
	config := StoreConfig{Endpoint: "http://localhost:1", SchemaName: "scope", RankingProfile: "rank", Space: fixtureSpace, Dimensions: 2}
	for _, limit := range []int64{-1, math.MaxInt64} {
		config.MaxResponseBytes = limit
		if err := config.Validate(); err == nil {
			t.Fatalf("unbounded response limit %d accepted", limit)
		}
	}
}

func TestUpsertSkipsVisitsAndReportsUnknownNativeWrite(t *testing.T) {
	store, fixture := newFixtureStore(t, StoreConfig{})
	fixture.visitResponse = func(*http.Request) any { t.Error("raw upsert enumerated unrelated records"); return visitResponse{} }
	fixture.putResponse = func(id string) any {
		if id == "uncertain" {
			id = "wrong"
		}
		return map[string]string{"id": store.nativeID(id)}
	}
	docs := []*document.Document{{ID: "ok", Text: "t"}, {ID: "uncertain", Text: "t"}, {ID: "later", Text: "t"}}
	err := store.Upsert(t.Context(), fixtureBatch(docs))
	e, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok || !slices.Equal(e.Outcome().Acknowledged, []string{"ok"}) || !slices.Equal(e.Outcome().Unconfirmed, []string{"uncertain"}) || !slices.Equal(e.Outcome().Unattempted, []string{"later"}) || fixture.writes != 2 {
		t.Fatalf("write evidence: %+v; writes %d", err, fixture.writes)
	}
}

func TestScanBudgetAppliesEvenToEmptyContinuationPages(t *testing.T) {
	store, fixture := newFixtureStore(t, StoreConfig{})
	reads := 0
	fixture.visitResponse = func(*http.Request) any {
		reads++
		return visitResponse{DocumentCount: new(0), Documents: []visitDocument{}, Continuation: fmt.Sprint(reads)}
	}
	budget := vectorstore.ScanBudget{MaxRecords: 2, MaxBytes: 120, Timeout: time.Second}
	hits, err := store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}}, budget)
	if hits != nil || !errors.Is(err, vectorstore.ErrScanBudget) || reads == 0 || reads > 4 || fixture.queries != 0 {
		t.Fatalf("hits=%v err=%v pages=%d queries=%d", hits, err, reads, fixture.queries)
	}
}

func TestProvenanceConformance(t *testing.T) {
	storetest.ProvenanceConformance(t, func(t *testing.T, doc *document.Document, predicate filter.Predicate) (*vectorstore.SearchResponse, error) {
		store, _ := newFixtureStore(t, StoreConfig{})
		if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{doc})); err != nil {
			return nil, err
		}
		return store.QueryByScan(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector(doc.Text), Options: vectorstore.SearchOptions{Filter: predicate}}, fixtureBudget)
	})
}
