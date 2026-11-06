package mariadb

import (
	"database/sql"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestNativeVectorAdmissionPrecedesWritesAndSearch(t *testing.T) {
	for _, test := range []struct {
		name   string
		metric DistanceMetric
		values []float64
	}{
		{"cosine zero", DistanceCosine, []float64{0, 0}},
		{"cosine signed zero", DistanceCosine, []float64{math.Copysign(0, -1), 0}},
		{"cosine narrowed zero", DistanceCosine, []float64{math.SmallestNonzeroFloat64, 0}},
		{"cosine nonzero with zero square", DistanceCosine, []float64{math.SmallestNonzeroFloat32, 0}},
		{"cosine square overflow", DistanceCosine, []float64{1e20, 0}},
		{"cosine squared norm overflow", DistanceCosine, []float64{math.Ldexp(1.5, 63), math.Ldexp(1.5, 63)}},
		{"euclidean square overflow", DistanceEuclidean, []float64{1e20, 0}},
		{"euclidean squared norm overflow", DistanceEuclidean, []float64{math.Ldexp(1.5, 63), math.Ldexp(1.5, 63)}},
		{"coordinate overflow", DistanceEuclidean, []float64{math.MaxFloat64, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			statement := &writeStmt{}
			rows := &resultRows{}
			db := sql.OpenDB(resultConnector{stmt: statement, rows: rows})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			store := offlineStore(t, db)
			store.distanceMetric = test.metric
			batch := fixtureBatch([]*document.Document{{ID: "first", Text: "valid"}, {ID: "last", Text: "invalid vector"}})
			batch.Records[1].Values = test.values
			if err := store.Upsert(t.Context(), batch); !errors.Is(err, vectorstore.ErrInvalidRequest) || len(statement.ids) != 0 {
				t.Errorf("upsert=%v writes=%v", err, statement.ids)
			}
			request := &vectorstore.Query{Space: fixtureSpace, Values: test.values}
			if response, err := store.Query(t.Context(), request); !errors.Is(err, vectorstore.ErrInvalidRequest) || response != nil || rows.closed {
				t.Errorf("query=%v error=%v search attempted=%t", response, err, rows.closed)
			}
			request.Options.Filter = filter.EQ("tenant", "alpha")
			if response, err := store.QueryByScan(t.Context(), request, fixtureBudget()); !errors.Is(err, vectorstore.ErrInvalidRequest) || response != nil {
				t.Errorf("scan=%v error=%v", response, err)
			}
		})
	}
}

func TestNativeVectorAdmissionPreservesMetricDomain(t *testing.T) {
	for _, test := range []struct {
		name   string
		metric DistanceMetric
		values []float64
	}{
		{"cosine unit", DistanceCosine, []float64{1, 0}},
		{"cosine one coordinate underflows", DistanceCosine, []float64{1, math.SmallestNonzeroFloat64}},
		{"cosine subnormal squared norm", DistanceCosine, []float64{math.Ldexp(1, -74), 0}},
		{"cosine large finite squared norm", DistanceCosine, []float64{math.Ldexp(1, 63), math.Ldexp(1, 63)}},
		{"euclidean zero", DistanceEuclidean, []float64{0, 0}},
		{"euclidean narrowed zero", DistanceEuclidean, []float64{math.SmallestNonzeroFloat64, 0}},
		{"euclidean nonzero with zero square", DistanceEuclidean, []float64{math.SmallestNonzeroFloat32, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			statement := &writeStmt{}
			db := sql.OpenDB(resultConnector{stmt: statement, rows: &resultRows{}})
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			store := offlineStore(t, db)
			store.distanceMetric = test.metric
			batch := fixtureBatch([]*document.Document{{ID: "document", Text: "text"}})
			batch.Records[0].Values = test.values
			if err := store.Upsert(t.Context(), batch); err != nil || len(statement.ids) != 1 {
				t.Fatalf("upsert=%v writes=%v", err, statement.ids)
			}
			request := &vectorstore.Query{Space: fixtureSpace, Values: test.values}
			if response, err := store.Query(t.Context(), request); err != nil || response == nil {
				t.Fatalf("query=%v error=%v", response, err)
			}
		})
	}
}
