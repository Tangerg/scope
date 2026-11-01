package pinecone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestCoreMetadataRoundTrips(t *testing.T) {
	for _, facts := range []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "integer": json.RawMessage(`9007199254740993`), "$key": json.RawMessage(`{"array":[1,null,true,{}]}`), "content": json.RawMessage(`"user value"`)}} {
		store, native := fixtureStore(t)
		doc := &document.Document{ID: "a/b?: #", Text: "text\n🙂", Metadata: facts}
		installDocuments(t, store, doc)
		if len(native.points[doc.ID].Metadata.Fields) != 3 {
			t.Fatal("metadata expanded into native fields")
		}
		response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")})
		if err != nil || len(response.Results) != 1 {
			t.Fatalf("response=%v error=%v", response, err)
		}
		got := response.Results[0].Document
		if got.ID != doc.ID || got.Text != doc.Text || !got.Metadata.Equal(facts) || (got.Metadata == nil) != (facts == nil) {
			t.Fatalf("changed document: %#v", got)
		}
	}
}

func TestNativeFilterConformance(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Unsupported: unsupportedCases(), Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		store, _ := fixtureStore(t)
		if err := store.Upsert(ctx, fixtureBatch(docs)); err != nil {
			return nil, err
		}
		request := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}}
		response, err := store.Query(ctx, request)
		if err != nil {
			return nil, err
		}
		selected := resultIDs(response)
		if err = store.DeleteWhere(ctx, predicate); err != nil {
			return nil, err
		}
		remaining, err := store.Query(ctx, request)
		if err != nil || len(remaining.Results) != 0 {
			return nil, errors.Join(err, errors.New("conditional delete retained selected records"))
		}
		return selected, nil
	}})
}

func TestIndexPreparesEveryRecordBeforePublishing(t *testing.T) {
	for _, failure := range []string{"later dimension", "FLOAT32 overflow", "later ID", "later metadata", "later media"} {
		t.Run(failure, func(t *testing.T) {
			store, native := fixtureStore(t)
			batch := fixtureBatch([]*document.Document{{ID: "one", Text: "first"}, {ID: "two", Text: "last"}})
			r := &batch.Records[1]
			switch failure {
			case "later dimension":
				r.Values = []float64{1}
			case "FLOAT32 overflow":
				r.Values[0] = math.MaxFloat64
			case "later ID":
				r.Document.ID = "invalid🙂"
			case "later metadata":
				r.Document.Metadata = metadata.Map{"bad": json.RawMessage("invalid")}
			case "later media":
				var err error
				r.Document.Media, err = media.NewBytes("image/png", []byte{1})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Upsert(t.Context(), batch); err == nil || len(native.upserts) != 0 {
				t.Fatalf("invalid record published: %v, %v", native.upserts, err)
			}
		})
	}
}

func TestIndexNativeBatchLimitsAndAcknowledgments(t *testing.T) {
	store, native := fixtureStore(t)
	var docs []*document.Document
	for i := range MaxVectorsPerUpsert + 1 {
		docs = append(docs, &document.Document{ID: fmt.Sprint(i), Text: "text"})
	}
	installDocuments(t, store, docs...)
	if len(native.upserts) != 2 || len(native.upserts[0]) != MaxVectorsPerUpsert || len(native.upserts[1]) != 1 {
		t.Fatalf("upserts=%v", native.upserts)
	}
	for _, count := range []uint32{0, 1, 3} {
		store, native = fixtureStore(t)
		native.upsert = func([]*pineconesdk.Vector) (uint32, error) { return count, nil }
		if err := store.Upsert(t.Context(), fixtureBatch(docs[:2])); err == nil {
			t.Fatalf("accepted acknowledgment %d", count)
		}
	}
	store, native = fixtureStore(t)
	native.upsert = func([]*pineconesdk.Vector) (uint32, error) { return 0, errNativeFailure }
	if err := store.Upsert(t.Context(), fixtureBatch(docs[:1])); !errors.Is(err, errNativeFailure) {
		t.Fatal(err)
	}
}

func TestIndexReportsGroupAcknowledgmentsWithoutInventingSuccesses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		count     uint32
		err       error
		wantCount *int
	}{
		{"partial", 1, nil, new(1)},
		{"zero", 0, nil, new(0)},
		{"malformed overcount", MaxVectorsPerUpsert + 1, nil, nil},
		{"response lost", 0, errNativeFailure, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, native := fixtureStore(t)
			ids := make([]string, 2*MaxVectorsPerUpsert+1)
			for i := range ids {
				ids[i] = fmt.Sprint(i)
			}
			var docs []*document.Document
			for _, id := range ids {
				docs = append(docs, &document.Document{ID: id, Text: id})
			}
			var calls int
			native.upsert = func(points []*pineconesdk.Vector) (uint32, error) {
				calls++
				if calls == 1 {
					return uint32(len(points)), nil
				}
				return tc.count, tc.err
			}
			err := store.Upsert(t.Context(), fixtureBatch(docs))
			w, ok := errors.AsType[*vectorstore.WriteError](err)
			if !ok || calls != 2 || !slices.Equal(w.Acknowledged, ids[:MaxVectorsPerUpsert]) ||
				!slices.Equal(w.Unconfirmed, ids[MaxVectorsPerUpsert:2*MaxVectorsPerUpsert]) || !slices.Equal(w.Unattempted, ids[2*MaxVectorsPerUpsert:]) {
				t.Fatalf("calls=%d write evidence=%+v, error=%v", calls, w, err)
			}
			if (w.AcknowledgedCount == nil) != (tc.wantCount == nil) || w.AcknowledgedCount != nil && *w.AcknowledgedCount != *tc.wantCount {
				t.Fatalf("unexpected aggregate acknowledgment: %v", w.AcknowledgedCount)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("lost native cause: %v", err)
			}
		})
	}
}

func TestSearchMergesNativeRankBeforeNormalizedScores(t *testing.T) {
	for _, metric := range []pineconesdk.IndexMetric{pineconesdk.IndexMetricCosine, pineconesdk.IndexMetricDotproduct, pineconesdk.IndexMetricEuclidean} {
		t.Run(string(metric), func(t *testing.T) {
			store, native := fixtureStore(t)
			store.schema.metric, native.metric = metric, metric
			var docs []*document.Document
			for i := range 101 {
				facts, err := metadata.FromValues(map[string]any{"value": "a", "unique": i})
				if err != nil {
					t.Fatal(err)
				}
				docs = append(docs, &document.Document{ID: fmt.Sprintf("id-%03d", i), Text: "text", Metadata: facts})
				native.scores[docs[i].ID] = 40
				if metric == pineconesdk.IndexMetricEuclidean {
					native.scores[docs[i].ID] = 100
				}
			}
			native.scores[docs[len(docs)-1].ID] = 50
			if metric == pineconesdk.IndexMetricEuclidean {
				native.scores[docs[len(docs)-1].ID] = 0
			}
			installDocuments(t, store, docs...)
			response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "a")}})
			if err != nil || !slices.Equal(resultIDs(response), []string{docs[len(docs)-1].ID}) || len(native.queries) != 1 {
				t.Fatalf("response=%v calls=%d error=%v", response, len(native.queries), err)
			}
			for _, request := range native.queries {
				if !request.IncludeMetadata || !request.IncludeValues || request.MetadataFilter == nil {
					t.Fatal("native rank did not request complete records and exact metadata")
				}
			}
		})
	}
}

func TestConditionalDeleteRetainsConcurrentMetadataChange(t *testing.T) {
	store, native := fixtureStore(t)
	facts, err := metadata.FromValues(map[string]any{"value": "a"})
	if err != nil {
		t.Fatal(err)
	}
	installDocuments(t, store, &document.Document{ID: "changed", Text: "text", Metadata: facts}, &document.Document{ID: "unchanged", Text: "text", Metadata: facts})
	native.beforeDelete = func() {
		changed, encodeErr := encodeRecord(&document.Document{ID: "changed", Text: "text", Metadata: metadata.Map{"value": []byte(`"b"`)}})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		native.points["changed"].Metadata = changed.Metadata
	}
	if err = store.DeleteWhere(t.Context(), filter.EQ("value", "a")); err != nil {
		t.Fatal(err)
	}
	if native.points["changed"] == nil || native.points["unchanged"] != nil || len(native.deletes) != 0 {
		t.Fatalf("deleted by stale identity: %v", native.points)
	}
	if err = store.DeleteWhere(t.Context(), nil); !errors.Is(err, vectorstore.ErrMissingFilter) {
		t.Fatal(err)
	}
	native.deleteErr = errNativeFailure
	if err = store.DeleteWhere(t.Context(), filter.EQ("value", "b")); !errors.Is(err, errNativeFailure) {
		t.Fatal(err)
	}
}

func TestDeleteIDsValidatesAllIdentitiesAndDeduplicates(t *testing.T) {
	for _, bad := range []string{"", "bad🙂", "bad\x00", strings.Repeat("x", 513)} {
		store, native := fixtureStore(t)
		ids := make([]string, maximumIDsPerDelete+1)
		for i := range ids {
			ids[i] = fmt.Sprint(i)
		}
		ids = append(ids, bad)
		if err := store.DeleteIDs(t.Context(), ids); err == nil || len(native.deletes) != 0 {
			t.Fatalf("partial delete: %v, %v", native.deletes, err)
		}
	}
	store, native := fixtureStore(t)
	var ids []string
	for i := range maximumIDsPerDelete + 1 {
		ids = append(ids, fmt.Sprint(i), fmt.Sprint(i))
	}
	if err := store.DeleteIDs(t.Context(), ids); err != nil {
		t.Fatal(err)
	}
	if len(native.deletes) != 2 || len(native.deletes[0]) != maximumIDsPerDelete || len(native.deletes[1]) != 1 {
		t.Fatalf("delete batches=%v", native.deletes)
	}
	native.deleteErr = errNativeFailure
	if err := store.DeleteIDs(t.Context(), []string{"unknown"}); !errors.Is(err, errNativeFailure) {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil || !native.closed {
		t.Fatalf("close=%v closed=%v", err, native.closed)
	}
}
