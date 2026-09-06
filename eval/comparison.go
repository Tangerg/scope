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

// MetricComparison keeps exact aggregate deltas attached to one full metric
// identity.
type MetricComparison struct {
	Metric           Metric
	Baseline         MetricSummary
	Candidate        MetricSummary
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

// Compare keeps the baseline authoritative: only reports over the same ordered
// Dataset and Metric identities are comparable. Exact deltas avoid inventing
// statistical significance or a synthetic score across unlike units.
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
		comparison.Metrics[index] = compareMetric(pair.baseline, pair.candidate)
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
	if len(baseline) != len(candidate) {
		return nil, fmt.Errorf("%w: metric count differs: baseline %d, candidate %d", ErrInvalidComparison, len(baseline), len(candidate))
	}
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
	pairs := make([]metricPair, len(baseline))
	for index := range baseline {
		baselineIdentity, err := baseline[index].Metric.identity()
		if err != nil {
			return nil, fmt.Errorf("%w: baseline metric %d: %w", ErrInvalidComparison, index, err)
		}
		candidateMetric, found := candidateByIdentity[baselineIdentity]
		if !found {
			return nil, fmt.Errorf(
				"%w: baseline metric %q is absent from candidate",
				ErrInvalidComparison, baseline[index].Metric,
			)
		}
		pairs[index] = metricPair{baseline: baseline[index], candidate: candidateMetric}
	}
	return pairs, nil
}

func compareMetric(baseline, candidate MetricSummary) MetricComparison {
	return MetricComparison{
		Metric:   baseline.Metric,
		Baseline: baseline, Candidate: candidate,
		EvaluatedDelta:   candidate.Evaluated - baseline.Evaluated,
		PassedDelta:      candidate.Passed - baseline.Passed,
		FailedDelta:      candidate.Failed - baseline.Failed,
		UnjudgedDelta:    candidate.Unjudged - baseline.Unjudged,
		ScoreDelta:       distributionDelta(baseline.Scores, candidate.Scores),
		MeasurementDelta: distributionDelta(baseline.Measurements, candidate.Measurements),
	}
}

func distributionDelta(baseline, candidate Distribution) DistributionDelta {
	if baseline.Count == 0 || candidate.Count == 0 {
		return DistributionDelta{}
	}
	return DistributionDelta{Present: true, Mean: candidate.Mean - baseline.Mean}
}
