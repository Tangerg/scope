package eval

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// CaseResult preserves all assessment outcomes for one fixed case. Result is
// the sole owner of assessment facts; case completion and verdict are derived.
type CaseResult struct {
	ID       CaseID
	Metadata metadata.Map
	Result   SuiteResult
}

func (c CaseResult) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	if err := c.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: case %q metadata: %w", ErrInvalidCase, c.ID, err)
	}
	return c.Result.Validate()
}

func (c CaseResult) clone() CaseResult {
	c.Metadata = c.Metadata.Clone()
	c.Result = c.Result.cloneValid()
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

// MetricSummary contains one top-level observation per successful case and
// assessment. Supporting Report.Details never become additional samples.
type MetricSummary struct {
	AssessmentID AssessmentID
	Metric       Metric
	Evaluated    int
	Passed       int
	Failed       int
	Unjudged     int
	Scores       Distribution
	Measurements Distribution
}

// AssessmentSummary preserves declared membership even if no evaluation
// produced a metric. Its four execution counts partition its case count.
type AssessmentSummary struct {
	ID           AssessmentID
	Completed    int
	Failed       int
	Canceled     int
	NotEvaluated int
}

// ExperimentSummary counts complete and incomplete cases separately. Partial
// counts cases with both successful and incomplete assessments; their valid
// observations remain in Metrics. Errors counts incomplete cases, not task
// quality failures. Assessment execution classifications remain in Assessments.
type ExperimentSummary struct {
	Total       int
	Evaluated   int
	Passed      int
	Failed      int
	Unjudged    int
	Errors      int
	Partial     int
	Assessments []AssessmentSummary
	Metrics     []MetricSummary
}

// ExperimentReport owns ordered case facts and a summary derived only from
// those facts. NewExperimentReport is also the import boundary for independently
// generated observations; it never executes a target or an evaluator.
type ExperimentReport struct {
	fixtureID string
	cases     []CaseResult
	summary   ExperimentSummary
}

func NewExperimentReport(fixtureID string, results []CaseResult) (ExperimentReport, error) {
	if fixtureID == "" || strings.TrimSpace(fixtureID) != fixtureID {
		return ExperimentReport{}, fmt.Errorf("%w: fixture identity is required", ErrInvalidExperiment)
	}
	owned := make([]CaseResult, len(results))
	seen := make(map[CaseID]struct{}, len(results))
	for index, result := range results {
		if err := result.Validate(); err != nil {
			return ExperimentReport{}, fmt.Errorf("%w: case %q: %w", ErrInvalidExperiment, result.ID, err)
		}
		if _, exists := seen[result.ID]; exists {
			return ExperimentReport{}, fmt.Errorf("%w: duplicate case %q", ErrInvalidExperiment, result.ID)
		}
		seen[result.ID] = struct{}{}
		owned[index] = result.clone()
	}
	summary, err := summarize(owned)
	if err != nil {
		return ExperimentReport{}, err
	}
	return ExperimentReport{fixtureID: fixtureID, cases: owned, summary: summary}, nil
}

func (e ExperimentReport) FixtureID() string { return e.fixtureID }

func (e ExperimentReport) Cases() []CaseResult {
	results := slices.Clone(e.cases)
	for index := range results {
		results[index] = results[index].clone()
	}
	return results
}

func (e ExperimentReport) Summary() ExperimentSummary {
	summary := e.summary
	summary.Assessments = slices.Clone(summary.Assessments)
	summary.Metrics = slices.Clone(summary.Metrics)
	return summary
}

// Compare requires the same fixed-input fixture and set of Case IDs, independent
// of declaration order. Numeric deltas pair observations by case, assessment,
// and calculation identity. Missing observations remain explicit. Decision
// deltas additionally require the same policy identity, so changing a threshold
// does not erase comparable scores or invent comparable verdicts.
func (e ExperimentReport) Compare(candidate ExperimentReport) (Comparison, error) {
	if e.fixtureID == "" || e.fixtureID != candidate.fixtureID {
		return Comparison{}, fmt.Errorf("%w: fixture identities are absent or differ", ErrInvalidComparison)
	}
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
		ErrorDelta:     candidateSummary.Errors - baselineSummary.Errors,
		Metrics:        make([]MetricComparison, len(metricPairs)),
	}
	for index, pair := range metricPairs {
		compared, err := pair.compare(e.cases, candidate.cases)
		if err != nil {
			return Comparison{}, err
		}
		comparison.Metrics[index] = compared
	}
	return comparison, nil
}

type observationKey struct {
	assessment AssessmentID
	metric     string
}

func summarize(results []CaseResult) (ExperimentSummary, error) {
	summary := ExperimentSummary{Total: len(results)}
	type accumulator struct {
		index        int
		scores       []float64
		measurements []float64
	}
	metrics := make(map[observationKey]*accumulator)
	assessments := make(map[AssessmentID]int)
	for _, result := range results {
		if result.Result.Complete() {
			summary.Evaluated++
			switch result.Result.Verdict() {
			case VerdictPass:
				summary.Passed++
			case VerdictFail:
				summary.Failed++
			default:
				summary.Unjudged++
			}
		} else {
			summary.Errors++
			for _, assessment := range result.Result.Results {
				if assessment.Status == AssessmentCompleted {
					summary.Partial++
					break
				}
			}
		}
		for _, assessment := range result.Result.Results {
			index, exists := assessments[assessment.ID]
			if !exists {
				index = len(summary.Assessments)
				assessments[assessment.ID] = index
				summary.Assessments = append(summary.Assessments, AssessmentSummary{ID: assessment.ID})
			}
			counts := &summary.Assessments[index]
			switch assessment.Status {
			case AssessmentFailed:
				counts.Failed++
				continue
			case AssessmentCanceled:
				counts.Canceled++
				continue
			case AssessmentNotEvaluated:
				counts.NotEvaluated++
				continue
			case AssessmentCompleted:
				counts.Completed++
			}
			report := assessment.Report
			identity, err := report.Metric.identity()
			if err != nil {
				return ExperimentSummary{}, fmt.Errorf("eval: summarize case %q assessment %q: %w", result.ID, assessment.ID, err)
			}
			key := observationKey{assessment: assessment.ID, metric: identity}
			current := metrics[key]
			if current == nil {
				current = &accumulator{index: len(summary.Metrics)}
				metrics[key] = current
				summary.Metrics = append(summary.Metrics, MetricSummary{AssessmentID: assessment.ID, Metric: report.Metric})
			}
			metricSummary := &summary.Metrics[current.index]
			metricSummary.Evaluated++
			switch report.Verdict() {
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
