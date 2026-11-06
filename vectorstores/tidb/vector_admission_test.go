package tidb

import (
	"database/sql"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestCosineVectorAdmissionPrecedesWritesAndSearch(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []float64
	}{
		{"zero", []float64{0, 0}},
		{"signed zero", []float64{math.Copysign(0, -1), 0}},
		{"float32 underflow", []float64{math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64}},
		{"nonzero coordinate with zero square", []float64{math.SmallestNonzeroFloat32, 0}},
		{"finite coordinate with infinite square", []float64{1e20, 0}},
		{"finite squares with infinite sum", []float64{math.Ldexp(1.5, 63), math.Ldexp(1.5, 63)}},
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
			batch := fixtureBatch([]*document.Document{{ID: "first", Text: "valid"}, {ID: "last", Text: "invalid vector"}})
			batch.Records[1].Values = test.values
			if err := store.Upsert(t.Context(), batch); !errors.Is(err, vectorstore.ErrInvalidRequest) || len(statement.ids) != 0 {
				t.Fatalf("upsert=%v writes=%v", err, statement.ids)
			}
			request := &vectorstore.Query{Space: fixtureSpace, Values: test.values}
			if response, err := store.Query(t.Context(), request); !errors.Is(err, vectorstore.ErrInvalidRequest) || response != nil || rows.closed {
				t.Fatalf("query=%v error=%v search attempted=%t", response, err, rows.closed)
			}
			request.Options.Filter = filter.EQ("tenant", "alpha")
			if response, err := store.QueryByScan(t.Context(), request, fixtureBudget()); !errors.Is(err, vectorstore.ErrInvalidRequest) || response != nil {
				t.Fatalf("scan=%v error=%v", response, err)
			}
		})
	}
}

func TestNativeVectorEncodingAppliesMetricAfterFloat32Conversion(t *testing.T) {
	for _, test := range []struct {
		name     string
		metric   DistanceMetric
		values   []float64
		accepted bool
	}{
		{"cosine nonzero", DistanceCosine, []float64{1, 0}, true},
		{"cosine one coordinate underflows", DistanceCosine, []float64{1, math.SmallestNonzeroFloat64}, true},
		{"cosine smallest float32 has zero square", DistanceCosine, []float64{math.SmallestNonzeroFloat32, 0}, false},
		{"cosine square rounds to zero", DistanceCosine, []float64{math.Ldexp(1, -75), 0}, false},
		{"cosine subnormal squared norm", DistanceCosine, []float64{math.Ldexp(1, -74), 0}, true},
		{"cosine large finite squared norm", DistanceCosine, []float64{math.Ldexp(1, 63), math.Ldexp(1, 63)}, true},
		{"cosine square overflow", DistanceCosine, []float64{math.Ldexp(1, 64), 0}, false},
		{"cosine squared norm sum overflow", DistanceCosine, []float64{math.Ldexp(1.5, 63), math.Ldexp(1.5, 63)}, false},
		{"L2 zero", DistanceL2, []float64{0, 0}, true},
		{"inner product zero", DistanceNegativeIP, []float64{0, 0}, true},
		{"L2 float32 underflow", DistanceL2, []float64{math.SmallestNonzeroFloat64, 0}, true},
		{"float32 overflow", DistanceCosine, []float64{math.MaxFloat64, 0}, false},
		{"native width", DistanceCosine, []float64{1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := test.metric.encodeVector(test.values, 2)
			if test.accepted {
				if err != nil || encoded == "" {
					t.Fatalf("encoded=%q error=%v", encoded, err)
				}
			} else if !errors.Is(err, vectorstore.ErrInvalidRequest) || encoded != "" {
				t.Fatalf("encoded=%q error=%v", encoded, err)
			}
		})
	}
}
