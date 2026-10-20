package postgres_test

import (
	"math"
	"testing"

	"github.com/Tangerg/scope/vectorstores/postgres"
)

func TestNativeVectorAndScoreContracts(t *testing.T) {
	for _, metric := range []postgres.DistanceMetric{postgres.DistanceCosine, postgres.DistanceL2, postgres.DistanceIP} {
		for _, vector := range [][]float64{{1}, {math.MaxFloat64, 0}, {math.SmallestNonzeroFloat64, 0}, {math.NaN(), 0}, {math.Inf(1), 0}} {
			if _, err := metric.Vector(vector, 2); err == nil {
				t.Fatalf("%s accepted %v", metric, vector)
			}
		}
		for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			if _, err := metric.Score(value); err == nil {
				t.Fatalf("%s accepted distance %v", metric, value)
			}
		}
	}
	if _, err := postgres.DistanceCosine.Vector([]float64{0, 0}, 2); err == nil {
		t.Fatal("zero cosine vector accepted")
	}
	if _, err := postgres.DistanceL2.Vector([]float64{0, 0}, 2); err != nil {
		t.Fatal(err)
	}
	for _, distance := range []float64{-1, 3} {
		if _, err := postgres.DistanceCosine.Score(distance); err == nil {
			t.Fatal("invalid cosine distance accepted")
		}
	}
	if _, err := postgres.DistanceL2.Score(-1); err == nil {
		t.Fatal("negative distance accepted")
	}
}

func TestDistanceMetricNativeProjection(t *testing.T) {
	for _, tc := range []struct {
		metric            postgres.DistanceMetric
		operator, opclass string
	}{
		{postgres.DistanceCosine, "<=>", "vector_cosine_ops"},
		{postgres.DistanceL2, "<->", "vector_l2_ops"},
		{postgres.DistanceIP, "<#>", "vector_ip_ops"},
	} {
		if !tc.metric.Valid() || tc.metric.String() != string(tc.metric) || tc.metric.Operator() != tc.operator || tc.metric.IndexOpClass() != tc.opclass {
			t.Fatalf("invalid native projection for %s", tc.metric)
		}
		score, err := tc.metric.Score(0)
		if err != nil || score.Validate() != nil {
			t.Fatalf("%s exact-match score = %v, %v", tc.metric, score, err)
		}
		vector, err := tc.metric.Vector([]float64{1, 0}, 2)
		if err != nil || vector.String() != "[1,0]" {
			t.Fatalf("%s vector = %s, %v", tc.metric, vector.String(), err)
		}
	}
	invalid := postgres.DistanceMetric("unknown")
	if invalid.Valid() {
		t.Fatal("unknown metric accepted")
	}
	if _, err := invalid.Score(0); err == nil {
		t.Fatal("unknown metric produced a score")
	}
	if _, err := invalid.Vector([]float64{1, 0}, 2); err == nil {
		t.Fatal("unknown metric admitted a vector")
	}
}
