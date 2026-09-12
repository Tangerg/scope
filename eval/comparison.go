package eval

import (
	"fmt"
)

// DistributionDelta is candidate mean minus baseline mean. Present is false
// when either side has no values, so absence cannot be mistaken for zero.
type DistributionDelta struct {
	Present bool
	Mean    float64
}

// MetricComparison relates observations of one full metric identity. A nil side
// means that run produced no reports for the metric, not a zero measurement.
// Count deltas treat absent observations as zero; numeric deltas require values
// on both sides. Different calculation rules always occupy distinct entries.
type MetricComparison struct {
	Metric           Metric
	Baseline         *MetricSummary
	Candidate        *MetricSummary
	EvaluatedDelta   int
	PassedDelta      int
	FailedDelta      int
	UnjudgedDelta    int
	ScoreDelta       DistributionDelta
	MeasurementDelta DistributionDelta
}

// Comparison reports candidate-minus-baseline deltas without inventing
// statistical significance.
type Comparison struct {
	Baseline       ExperimentSummary
	Candidate      ExperimentSummary
	EvaluatedDelta int
	PassedDelta    int
	FailedDelta    int
	UnjudgedDelta  int
	ErrorDelta     int
	Metrics        []MetricComparison
}

func comparableCases(baseline, candidate []CaseResult) error {
	if len(baseline) != len(candidate) {
		return fmt.Errorf("%w: case count differs: baseline %d, candidate %d", ErrInvalidComparison, len(baseline), len(candidate))
	}
	for index := range baseline {
		if baseline[index].ID != candidate[index].ID {
			return fmt.Errorf(
				"%w: case %d identity differs: baseline %q, candidate %q",
				ErrInvalidComparison, index, baseline[index].ID, candidate[index].ID,
			)
		}
	}
	return nil
}

// metricPair retains catalog membership independently of observation counts.
type metricPair struct {
	baseline  *MetricSummary
	candidate *MetricSummary
}

func comparableMetrics(baseline, candidate []MetricSummary) ([]metricPair, error) {
	candidateByIdentity := make(map[string]*MetricSummary, len(candidate))
	for index := range candidate {
		identity, err := candidate[index].Metric.identity()
		if err != nil {
			return nil, fmt.Errorf("%w: candidate metric %d: %w", ErrInvalidComparison, index, err)
		}
		if _, duplicate := candidateByIdentity[identity]; duplicate {
			return nil, fmt.Errorf("%w: duplicate candidate metric %q", ErrInvalidComparison, candidate[index].Metric)
		}
		candidateByIdentity[identity] = &candidate[index]
	}
	pairs := make([]metricPair, 0, len(baseline)+len(candidate))
	for index := range baseline {
		baselineIdentity, err := baseline[index].Metric.identity()
		if err != nil {
			return nil, fmt.Errorf("%w: baseline metric %d: %w", ErrInvalidComparison, index, err)
		}
		candidateMetric := candidateByIdentity[baselineIdentity]
		pairs = append(pairs, metricPair{baseline: &baseline[index], candidate: candidateMetric})
		delete(candidateByIdentity, baselineIdentity)
	}
	for index := range candidate {
		identity, err := candidate[index].Metric.identity()
		if err != nil {
			return nil, err
		}
		if remaining, found := candidateByIdentity[identity]; found {
			pairs = append(pairs, metricPair{candidate: remaining})
		}
	}
	return pairs, nil
}

func (m metricPair) compare() (MetricComparison, error) {
	var baseline, candidate MetricSummary
	comparison := MetricComparison{}
	if m.baseline != nil {
		baseline = *m.baseline
		comparison.Metric = baseline.Metric
		comparison.Baseline = &baseline
	}
	if m.candidate != nil {
		candidate = *m.candidate
		comparison.Metric = candidate.Metric
		comparison.Candidate = &candidate
	}
	scoreDelta, err := baseline.Scores.delta(candidate.Scores)
	if err != nil {
		return MetricComparison{}, fmt.Errorf("eval: compare metric %q scores: %w", comparison.Metric, err)
	}
	measurementDelta, err := baseline.Measurements.delta(candidate.Measurements)
	if err != nil {
		return MetricComparison{}, fmt.Errorf("eval: compare metric %q measurements: %w", comparison.Metric, err)
	}
	comparison.EvaluatedDelta = candidate.Evaluated - baseline.Evaluated
	comparison.PassedDelta = candidate.Passed - baseline.Passed
	comparison.FailedDelta = candidate.Failed - baseline.Failed
	comparison.UnjudgedDelta = candidate.Unjudged - baseline.Unjudged
	comparison.ScoreDelta = scoreDelta
	comparison.MeasurementDelta = measurementDelta
	return comparison, nil
}
