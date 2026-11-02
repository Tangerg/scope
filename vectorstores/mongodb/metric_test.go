package mongodb

import (
	"math"
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type metricVectorCase struct {
	name   string
	metric string
	values []float64
	valid  bool
}

func metricVectorCases() []metricVectorCase {
	return []metricVectorCase{
		{name: "cosine zero", metric: "cosine", values: []float64{0, 0}},
		{name: "cosine FLOAT32 underflow", metric: "cosine", values: []float64{math.SmallestNonzeroFloat64, 0}},
		{name: "cosine nonunit", metric: "cosine", values: []float64{2, 0}, valid: true},
		{name: "dot product zero", metric: "dotProduct", values: []float64{0, 0}},
		{name: "dot product nonunit", metric: "dotProduct", values: []float64{2, 0}},
		{name: "dot product unit", metric: "dotProduct", values: []float64{1, 0}, valid: true},
		{name: "dot product rounded coordinates", metric: "dotProduct", values: []float64{0.6, 0.8}, valid: true},
		{name: "dot product one ULP", metric: "dotProduct", values: []float64{float64(math.Nextafter32(1, 2)), 0}, valid: true},
		{name: "dot product beyond one ULP", metric: "dotProduct", values: []float64{float64(math.Nextafter32(math.Nextafter32(1, 2), 2)), 0}},
		{name: "euclidean zero", metric: "euclidean", values: []float64{0, 0}, valid: true},
		{name: "euclidean nonunit", metric: "euclidean", values: []float64{2, 0}, valid: true},
	}
}

func TestNativeMetricAdmitsWholeBatchAndQueryBeforeIO(t *testing.T) {
	for _, sample := range metricVectorCases() {
		t.Run(sample.name, func(t *testing.T) {
			collection := &filterCollection{policy: []any{nativeIndexPolicy(DefaultVectorIndexName, sample.metric, 2)}}
			store := upsertStore(t, collection)
			batch := fixtureBatch([]*document.Document{{ID: "first", Text: "valid"}, {ID: "second", Text: "candidate"}})
			batch.Records[1].Values = slices.Clone(sample.values)
			err := store.Upsert(t.Context(), batch)
			if (err == nil) != sample.valid {
				t.Fatalf("Upsert error=%v, valid=%t", err, sample.valid)
			}
			if !slices.Equal(batch.Records[1].Values, sample.values) {
				t.Fatal("Upsert changed the host vector")
			}
			if !sample.valid && (collection.writes != 0 || len(collection.records) != 0) {
				t.Fatalf("invalid second record partially mutated the batch: writes=%d records=%d", collection.writes, len(collection.records))
			}
			if sample.valid {
				if collection.writes != 1 || len(collection.records) != 2 {
					t.Fatalf("valid batch: writes=%d records=%d", collection.writes, len(collection.records))
				}
				stored, decodeErr := collection.records[1].Lookup(fixtureSpace.StorageKey()).Array().Values()
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				for index, value := range stored {
					if value.Double() != float64(float32(sample.values[index])) {
						t.Fatalf("stored vector was normalized: %v", stored)
					}
				}
			}
			for _, scan := range []bool{false, true} {
				queryCalled := false
				collection.beforeQuery = func() { queryCalled = true }
				request := &vectorstore.Query{Space: fixtureSpace, Values: slices.Clone(sample.values)}
				var response *vectorstore.SearchResponse
				if scan {
					request.Options.Filter = filter.IsNull("absent")
					response, err = store.Scan(t.Context(), request, fixtureBudget())
				} else {
					response, err = store.Query(t.Context(), request)
				}
				if (err == nil) != sample.valid || queryCalled != sample.valid {
					t.Fatalf("scan=%t error=%v native_query=%t, valid=%t", scan, err, queryCalled, sample.valid)
				}
				if !sample.valid && response != nil {
					t.Fatalf("invalid vector published a response: %+v", response)
				}
				if !slices.Equal(request.Values, sample.values) {
					t.Fatal("query changed the host vector")
				}
			}
		})
	}
}

func TestNativeMetricRejectsStoredVectorsBeforePublicationOrDeletion(t *testing.T) {
	for _, sample := range metricVectorCases() {
		t.Run(sample.name, func(t *testing.T) {
			var record bson.M
			if err := bson.Unmarshal(nativeRecord(t, "stored", "null"), &record); err != nil {
				t.Fatal(err)
			}
			record[fixtureSpace.StorageKey()] = sample.values
			raw, err := bson.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			collection := &filterCollection{
				policy:  []any{nativeIndexPolicy(DefaultVectorIndexName, sample.metric, 2)},
				records: []bson.Raw{raw},
			}
			store := upsertStore(t, collection)
			for _, scan := range []bool{false, true} {
				request := &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}}
				var response *vectorstore.SearchResponse
				if scan {
					request.Options.Filter = filter.IsNull("absent")
					response, err = store.Scan(t.Context(), request, fixtureBudget())
				} else {
					response, err = store.Query(t.Context(), request)
				}
				if (err == nil) != sample.valid || !sample.valid && response != nil {
					t.Fatalf("scan=%t response=%+v error=%v, valid=%t", scan, response, err, sample.valid)
				}
				if sample.valid && (response == nil || len(response.Results) != 1 || response.Results[0].Document.ID != "stored") {
					t.Fatalf("valid stored vector lost its document: %+v", response)
				}
			}
			err = store.DeleteMatching(t.Context(), filter.IsNull("absent"), fixtureBudget())
			if (err == nil) != sample.valid || !sample.valid && len(collection.deleted) != 0 {
				t.Fatalf("DeleteMatching error=%v deleted=%v, valid=%t", err, collection.deleted, sample.valid)
			}
			if sample.valid && !slices.Equal(collection.deleted, []string{"stored"}) {
				t.Fatalf("valid stored vector could not be deleted: %v", collection.deleted)
			}
		})
	}
}
