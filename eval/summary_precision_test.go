package eval_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/Tangerg/scope/eval"
)

func TestSummaryAndComparisonPreserveRepresentableMeans(t *testing.T) {
	cancellationMagnitude := math.Ldexp(1, 54)
	tinyResidual := 1e-100
	for _, test := range []struct {
		name   string
		values []float64
		mean   float64
	}{
		{name: "positive cancellation residual", values: []float64{-cancellationMagnitude, 1, cancellationMagnitude}, mean: 1.0 / 3.0},
		{name: "negative cancellation residual", values: []float64{-cancellationMagnitude, -1, cancellationMagnitude}, mean: -1.0 / 3.0},
		{name: "rounding cancellation residual", values: []float64{-1e16, 1, 1e16}, mean: 1.0 / 3.0},
		{name: "residual lost by scaling", values: []float64{-math.MaxFloat64, tinyResidual, math.MaxFloat64}, mean: tinyResidual / 3},
		{name: "subnormal cancellation residual", values: []float64{-math.MaxFloat64, 3 * math.SmallestNonzeroFloat64, math.MaxFloat64}, mean: math.SmallestNonzeroFloat64},
		{name: "same sign maximum", values: []float64{math.MaxFloat64, math.MaxFloat64, math.MaxFloat64}, mean: math.MaxFloat64},
		{name: "same sign subnormal", values: []float64{math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64}, mean: math.SmallestNonzeroFloat64},
		{name: "subnormal rounds to nearest even", values: []float64{3 * math.SmallestNonzeroFloat64, 0}, mean: 2 * math.SmallestNonzeroFloat64},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := reportMeasurements(t, test.values...)
			measurement := report.Summary().Metrics[0].Measurements
			if measurement.Count != len(test.values) || measurement.Mean != test.mean {
				t.Errorf("summary count=%d mean=%.17g; want count=%d mean=%.17g", measurement.Count, measurement.Mean, len(test.values), test.mean)
			}
			baseline := reportMeasurements(t, make([]float64, len(test.values))...)
			comparison, err := baseline.Compare(report)
			if err != nil {
				t.Fatal(err)
			}
			delta := comparison.Metrics[0].MeasurementDelta
			if !delta.Present || delta.Matched != len(test.values) || delta.Mean != test.mean {
				t.Errorf("paired measurements=%+v; want matched=%d mean=%.17g", delta, len(test.values), test.mean)
			}
		})
	}
}

func reportMeasurements(t *testing.T, measurements ...float64) eval.ExperimentReport {
	t.Helper()
	metric := testMetric("measurement")
	results := make([]eval.CaseResult, len(measurements))
	for index, value := range measurements {
		results[index] = eval.CaseResult{
			ID: eval.CaseID(fmt.Sprintf("case-%d", index)),
			Result: eval.SuiteResult{Results: []eval.AssessmentResult{{
				ID: "measurement", Report: &eval.Report{Metric: metric, Measurement: &value},
			}}},
		}
	}
	report, err := eval.NewExperimentReport("measurement-fixture", results)
	if err != nil {
		t.Fatal(err)
	}
	return report
}
