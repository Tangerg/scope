package eval

import (
	"fmt"
	"math"
)

// DistributionDelta summarizes candidate-minus-baseline differences for
// matched case observations. Unmatched observations are counted, never imputed.
type DistributionDelta struct {
	Present       bool
	Mean          float64
	Matched       int
	BaselineOnly  int
	CandidateOnly int
}

// DecisionDelta compares only matched decisions produced by the same policy.
// Incompatible counts pairs whose policies differ, including threshold changes.
type DecisionDelta struct {
	Matched       int
	BaselineOnly  int
	CandidateOnly int
	Incompatible  int
	PassedDelta   int
	FailedDelta   int
}

// MetricComparison relates the same named assessment and calculation. Numeric
// deltas use paired cases, while the two summaries retain all observations.
type MetricComparison struct {
	AssessmentID     AssessmentID
	Metric           Metric
	Baseline         *MetricSummary
	Candidate        *MetricSummary
	EvaluatedDelta   int
	ScoreDelta       DistributionDelta
	MeasurementDelta DistributionDelta
	DecisionDelta    DecisionDelta
}

// Comparison preserves execution coverage and paired quality differences.
// It makes no claim about statistical significance.
type Comparison struct {
	Baseline       ExperimentSummary
	Candidate      ExperimentSummary
	EvaluatedDelta int
	ErrorDelta     int
	Metrics        []MetricComparison
}

func comparableCases(baseline, candidate []CaseResult) error {
	if len(baseline) != len(candidate) {
		return fmt.Errorf("%w: case count differs: baseline %d, candidate %d", ErrInvalidComparison, len(baseline), len(candidate))
	}
	identities := make(map[CaseID]struct{}, len(candidate))
	for _, result := range candidate {
		identities[result.ID] = struct{}{}
	}
	for _, result := range baseline {
		if _, exists := identities[result.ID]; !exists {
			return fmt.Errorf("%w: candidate is missing case %q", ErrInvalidComparison, result.ID)
		}
	}
	return nil
}

type metricPair struct {
	baseline  *MetricSummary
	candidate *MetricSummary
}

func comparableMetrics(baseline, candidate []MetricSummary) ([]metricPair, error) {
	candidateByIdentity := make(map[observationKey]*MetricSummary, len(candidate))
	for index := range candidate {
		identity, err := candidate[index].Metric.identity()
		if err != nil {
			return nil, fmt.Errorf("%w: candidate metric %d: %w", ErrInvalidComparison, index, err)
		}
		key := observationKey{assessment: candidate[index].AssessmentID, metric: identity}
		candidateByIdentity[key] = &candidate[index]
	}
	pairs := make([]metricPair, 0, len(baseline)+len(candidate))
	for index := range baseline {
		identity, err := baseline[index].Metric.identity()
		if err != nil {
			return nil, fmt.Errorf("%w: baseline metric %d: %w", ErrInvalidComparison, index, err)
		}
		key := observationKey{assessment: baseline[index].AssessmentID, metric: identity}
		pairs = append(pairs, metricPair{baseline: &baseline[index], candidate: candidateByIdentity[key]})
		delete(candidateByIdentity, key)
	}
	for index := range candidate {
		identity, err := candidate[index].Metric.identity()
		if err != nil {
			return nil, err
		}
		key := observationKey{assessment: candidate[index].AssessmentID, metric: identity}
		if remaining, found := candidateByIdentity[key]; found {
			pairs = append(pairs, metricPair{candidate: remaining})
		}
	}
	return pairs, nil
}

func (m metricPair) compare(baselineCases, candidateCases []CaseResult) (MetricComparison, error) {
	var baseline, candidate MetricSummary
	comparison := MetricComparison{}
	if m.baseline != nil {
		baseline = *m.baseline
		comparison.Metric, comparison.AssessmentID = baseline.Metric, baseline.AssessmentID
		comparison.Baseline = &baseline
	}
	if m.candidate != nil {
		candidate = *m.candidate
		comparison.Metric, comparison.AssessmentID = candidate.Metric, candidate.AssessmentID
		comparison.Candidate = &candidate
	}
	identity, err := comparison.Metric.identity()
	if err != nil {
		return MetricComparison{}, err
	}
	key := observationKey{assessment: comparison.AssessmentID, metric: identity}
	left, err := caseObservations(baselineCases, key)
	if err != nil {
		return MetricComparison{}, err
	}
	right, err := caseObservations(candidateCases, key)
	if err != nil {
		return MetricComparison{}, err
	}
	comparison.ScoreDelta, err = signalScore.compare(baselineCases, left, right)
	if err != nil {
		return MetricComparison{}, fmt.Errorf("eval: compare metric %q scores: %w", comparison.Metric, err)
	}
	comparison.MeasurementDelta, err = signalMeasurement.compare(baselineCases, left, right)
	if err != nil {
		return MetricComparison{}, fmt.Errorf("eval: compare metric %q measurements: %w", comparison.Metric, err)
	}
	comparison.DecisionDelta, err = compareDecisions(baselineCases, left, right)
	if err != nil {
		return MetricComparison{}, err
	}
	comparison.EvaluatedDelta = candidate.Evaluated - baseline.Evaluated
	return comparison, nil
}

func caseObservations(cases []CaseResult, key observationKey) (map[CaseID]*Report, error) {
	observations := make(map[CaseID]*Report, len(cases))
	for _, result := range cases {
		for _, assessment := range result.Result.Results {
			if assessment.ID != key.assessment || assessment.Status() != AssessmentCompleted {
				continue
			}
			identity, err := assessment.Report.Metric.identity()
			if err != nil {
				return nil, err
			}
			if identity == key.metric {
				observations[result.ID] = assessment.Report
			}
		}
	}
	return observations, nil
}

type numericSignal uint8

const (
	signalScore numericSignal = iota
	signalMeasurement
)

func (n numericSignal) value(report *Report) (float64, bool) {
	if report == nil {
		return 0, false
	}
	if n == signalScore && report.Score != nil {
		return report.Score.Float64(), true
	}
	if n == signalMeasurement && report.Measurement != nil {
		return *report.Measurement, true
	}
	return 0, false
}

func (n numericSignal) compare(cases []CaseResult, baseline, candidate map[CaseID]*Report) (DistributionDelta, error) {
	result := DistributionDelta{}
	var differences []float64
	for _, caseValue := range cases {
		left, hasLeft := n.value(baseline[caseValue.ID])
		right, hasRight := n.value(candidate[caseValue.ID])
		switch {
		case hasLeft && hasRight:
			difference := right - left
			if math.IsInf(difference, 0) || math.IsNaN(difference) {
				return DistributionDelta{}, fmt.Errorf("%w: case %q difference overflows float64", ErrInvalidComparison, caseValue.ID)
			}
			result.Matched++
			differences = append(differences, difference)
		case hasLeft:
			result.BaselineOnly++
		case hasRight:
			result.CandidateOnly++
		}
	}
	if len(differences) > 0 {
		result.Present = true
		result.Mean = distribution(differences).Mean
	}
	return result, nil
}

func compareDecisions(cases []CaseResult, baseline, candidate map[CaseID]*Report) (DecisionDelta, error) {
	result := DecisionDelta{}
	for _, caseValue := range cases {
		left, right := decidedReport(baseline[caseValue.ID]), decidedReport(candidate[caseValue.ID])
		switch {
		case left != nil && right != nil:
			if err := result.pair(*left, *right); err != nil {
				return DecisionDelta{}, err
			}
		case left != nil:
			result.BaselineOnly++
		case right != nil:
			result.CandidateOnly++
		}
	}
	return result, nil
}

func decidedReport(report *Report) *Report {
	if report == nil || report.Decision == nil {
		return nil
	}
	return report
}

// pair relies on a Decision verdict being exactly pass or fail, so one pass
// verdict moving between runs shifts both counts.
func (d *DecisionDelta) pair(baseline, candidate Report) error {
	baselineIdentity, err := baseline.decisionIdentity()
	if err != nil {
		return err
	}
	candidateIdentity, err := candidate.decisionIdentity()
	if err != nil {
		return err
	}
	if baselineIdentity != candidateIdentity {
		d.Incompatible++
		return nil
	}
	d.Matched++
	if baseline.Verdict() == VerdictPass {
		d.PassedDelta--
		d.FailedDelta++
	}
	if candidate.Verdict() == VerdictPass {
		d.PassedDelta++
		d.FailedDelta--
	}
	return nil
}
