package eval

import (
	"fmt"
	"math"
	"slices"

	"github.com/Tangerg/scope/core/metadata"
)

// CaseResult preserves Dataset identity whether evaluation produced a report or
// an error.
type CaseResult struct {
	ID       CaseID
	Metadata metadata.Map
	Report   Report
	Err      error
}

func (c CaseResult) clone() CaseResult {
	c.Metadata = c.Metadata.Clone()
	if c.Err == nil {
		c.Report = c.Report.cloneValid()
	}
	return c
}

// Distribution summarizes one homogeneous numeric signal. Count distinguishes
// an absent distribution from a real distribution whose values are all zero.
type Distribution struct {
	Count   int
	Mean    float64
	Minimum float64
	P10     float64
	P50     float64
	P90     float64
	Maximum float64
}

// MetricSummary keeps score and measurement distributions attached to their
// full Metric identity so unrelated units, directions, and configurations are
// never aggregated together. Experiment summarizes both top-level reports and
// their Details.
type MetricSummary struct {
	Metric       Metric
	Evaluated    int
	Passed       int
	Failed       int
	Unjudged     int
	Scores       Distribution
	Measurements Distribution
}

// ExperimentSummary aggregates categorical outcomes and homogeneous metric
// distributions without collapsing unlike metrics.
type ExperimentSummary struct {
	Total     int
	Evaluated int
	Passed    int
	Failed    int
	Unjudged  int
	Errors    int
	Metrics   []MetricSummary
}

// ExperimentReport owns ordered case results and the summary derived from them.
type ExperimentReport struct {
	cases   []CaseResult
	summary ExperimentSummary
}

// Cases returns owned results in Dataset order.
func (e ExperimentReport) Cases() []CaseResult {
	results := slices.Clone(e.cases)
	for index := range results {
		results[index] = results[index].clone()
	}
	return results
}

// Summary returns the owned aggregate calculated from Cases.
func (e ExperimentReport) Summary() ExperimentSummary {
	summary := e.summary
	summary.Metrics = slices.Clone(summary.Metrics)
	return summary
}

func newCaseResults[T any](cases []Case[T]) []CaseResult {
	results := make([]CaseResult, len(cases))
	for index, caseValue := range cases {
		results[index] = CaseResult{ID: caseValue.ID, Metadata: caseValue.Metadata.Clone()}
	}
	return results
}

func summarize(results []CaseResult) (ExperimentSummary, error) {
	summary := ExperimentSummary{Total: len(results)}
	type accumulator struct {
		index        int
		scores       []float64
		measurements []float64
	}
	metrics := make(map[string]*accumulator)
	var summarizeMetric func(Report) error
	summarizeMetric = func(report Report) error {
		identity, err := report.Metric.identity()
		if err != nil {
			return err
		}
		current := metrics[identity]
		if current == nil {
			current = &accumulator{index: len(summary.Metrics)}
			metrics[identity] = current
			summary.Metrics = append(summary.Metrics, MetricSummary{Metric: report.Metric})
		}
		metricSummary := &summary.Metrics[current.index]
		metricSummary.Evaluated++
		switch report.Verdict {
		case VerdictPass:
			metricSummary.Passed++
		case VerdictFail:
			metricSummary.Failed++
		default:
			metricSummary.Unjudged++
		}
		if report.Score != nil {
			current.scores = append(current.scores, report.Score.Float64())
		}
		if report.Measurement != nil {
			current.measurements = append(current.measurements, *report.Measurement)
		}
		for _, detail := range report.Details {
			if err := summarizeMetric(detail); err != nil {
				return err
			}
		}
		return nil
	}
	for _, result := range results {
		if result.Err != nil {
			summary.Errors++
			continue
		}
		if err := result.Report.Validate(); err != nil {
			return ExperimentSummary{}, fmt.Errorf("eval: summarize case %q: %w", result.ID, err)
		}
		summary.Evaluated++
		switch result.Report.Verdict {
		case VerdictPass:
			summary.Passed++
		case VerdictFail:
			summary.Failed++
		default:
			summary.Unjudged++
		}

		if err := summarizeMetric(result.Report); err != nil {
			return ExperimentSummary{}, fmt.Errorf("eval: summarize case %q: %w", result.ID, err)
		}
	}
	for _, current := range metrics {
		metricSummary := &summary.Metrics[current.index]
		metricSummary.Scores = distribution(current.scores)
		metricSummary.Measurements = distribution(current.measurements)
	}
	return summary, nil
}

func distribution(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	slices.Sort(values)
	result := Distribution{
		Count: len(values), Minimum: values[0], Maximum: values[len(values)-1],
		P10: percentile(values, 0.10), P50: percentile(values, 0.50), P90: percentile(values, 0.90),
	}
	scale := max(math.Abs(result.Minimum), math.Abs(result.Maximum))
	if scale == 0 {
		return result
	}
	// The mean is bounded by the inputs even when their sum would overflow.
	for _, value := range values {
		result.Mean += value / scale
	}
	result.Mean = result.Mean / float64(len(values)) * scale
	return result
}

func percentile(sorted []float64, quantile float64) float64 {
	index := max(0, int(math.Ceil(quantile*float64(len(sorted))))-1)
	return sorted[index]
}
