package eval

import (
	"fmt"
	"math"
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

// Compare compares runs over the same ordered Dataset identities. Execution
// counts remain comparable when evaluation fails. Metrics are matched by full
// identity in baseline order, followed by candidate-only metrics; an absent
// side remains explicit instead of preventing comparison of the whole run.
// A mean difference outside the finite float64 range returns ErrInvalidComparison
// without exposing a partial comparison.
func (e ExperimentReport) Compare(candidate ExperimentReport) (Comparison, error) {
	if err := comparableCases(e.cases, candidate.cases); err != nil {
		return Comparison{}, err
	}
	baselineSummary, candidateSummary := e.Summary(), candidate.Summary()
	metricPairs, err := comparableMetrics(baselineSummary.Metrics, candidateSummary.Metrics)
	if err != nil {
		return Comparison{}, err
	}
	comparison := Comparison{
		Baseline: baselineSummary, Candidate: candidateSummary,
		EvaluatedDelta: candidateSummary.Evaluated - baselineSummary.Evaluated,
		PassedDelta:    candidateSummary.Passed - baselineSummary.Passed,
		FailedDelta:    candidateSummary.Failed - baselineSummary.Failed,
		UnjudgedDelta:  candidateSummary.Unjudged - baselineSummary.Unjudged,
		ErrorDelta:     candidateSummary.Errors - baselineSummary.Errors,
		Metrics:        make([]MetricComparison, len(metricPairs)),
	}
	for index, pair := range metricPairs {
		metricComparison, err := compareMetric(pair.baseline, pair.candidate)
		if err != nil {
			return Comparison{}, err
		}
		comparison.Metrics[index] = metricComparison
	}
	return comparison, nil
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

type metricPair struct {
	baseline  MetricSummary
	candidate MetricSummary
}

func comparableMetrics(baseline, candidate []MetricSummary) ([]metricPair, error) {
	candidateByIdentity := make(map[string]MetricSummary, len(candidate))
	for index := range candidate {
		identity, err := candidate[index].Metric.identity()
		if err != nil {
			return nil, fmt.Errorf("%w: candidate metric %d: %w", ErrInvalidComparison, index, err)
		}
		if _, duplicate := candidateByIdentity[identity]; duplicate {
			return nil, fmt.Errorf("%w: duplicate candidate metric %q", ErrInvalidComparison, candidate[index].Metric)
		}
		candidateByIdentity[identity] = candidate[index]
	}
	pairs := make([]metricPair, 0, len(baseline)+len(candidate))
	for index := range baseline {
		baselineIdentity, err := baseline[index].Metric.identity()
		if err != nil {
			return nil, fmt.Errorf("%w: baseline metric %d: %w", ErrInvalidComparison, index, err)
		}
		candidateMetric := candidateByIdentity[baselineIdentity]
		pairs = append(pairs, metricPair{baseline: baseline[index], candidate: candidateMetric})
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

func compareMetric(baseline, candidate MetricSummary) (MetricComparison, error) {
	scoreDelta, err := distributionDelta(baseline.Scores, candidate.Scores)
	if err != nil {
		return MetricComparison{}, fmt.Errorf("eval: compare metric %q scores: %w", baseline.Metric, err)
	}
	measurementDelta, err := distributionDelta(baseline.Measurements, candidate.Measurements)
	if err != nil {
		return MetricComparison{}, fmt.Errorf("eval: compare metric %q measurements: %w", baseline.Metric, err)
	}
	comparison := MetricComparison{
		EvaluatedDelta:   candidate.Evaluated - baseline.Evaluated,
		PassedDelta:      candidate.Passed - baseline.Passed,
		FailedDelta:      candidate.Failed - baseline.Failed,
		UnjudgedDelta:    candidate.Unjudged - baseline.Unjudged,
		ScoreDelta:       scoreDelta,
		MeasurementDelta: measurementDelta,
	}
	if baseline.Evaluated > 0 {
		comparison.Metric = baseline.Metric
		comparison.Baseline = &baseline
	}
	if candidate.Evaluated > 0 {
		comparison.Metric = candidate.Metric
		comparison.Candidate = &candidate
	}
	return comparison, nil
}

func distributionDelta(baseline, candidate Distribution) (DistributionDelta, error) {
	if baseline.Count == 0 || candidate.Count == 0 {
		return DistributionDelta{}, nil
	}
	difference := candidate.Mean - baseline.Mean
	if math.IsInf(difference, 0) {
		return DistributionDelta{}, fmt.Errorf("%w: mean difference overflows float64", ErrInvalidComparison)
	}
	return DistributionDelta{Present: true, Mean: difference}, nil
}
