package eval

import (
	"fmt"
	"math"
	"slices"

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
// assessment. Supporting Report.Details never become additional samples; their
// Metrics, with Metric, identify which observations the summary groups.
type MetricSummary struct {
	AssessmentID AssessmentID
	Metric       Metric
	Details      []DetailMetric
	Evaluated    int
	Passed       int
	Failed       int
	Unjudged     int
	Scores       Distribution
	Measurements Distribution
}

func (m MetricSummary) identity() (string, error) {
	return observationIdentity(m.Metric, m.Details)
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

// ExperimentSummary counts complete and incomplete cases separately. Errors
// counts incomplete cases, not quality failures; Partial counts the incomplete
// cases whose completed assessments still contribute to Metrics.
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

// ExperimentReport owns ordered case facts and the summary derived from them.
// NewExperimentReport also imports independently generated observations.
type ExperimentReport struct {
	fixtureID string
	cases     []CaseResult
	summary   ExperimentSummary
}

func NewExperimentReport(fixtureID string, results []CaseResult) (ExperimentReport, error) {
	if err := validateFixtureID(fixtureID); err != nil {
		return ExperimentReport{}, fmt.Errorf("%w: %w", ErrInvalidExperiment, err)
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
	for index := range summary.Metrics {
		summary.Metrics[index].Details = cloneDetailMetrics(summary.Metrics[index].Details)
	}
	return summary
}

// Compare requires the same fixture and set of Case IDs, in any order. A
// changed threshold keeps scores comparable but makes decisions incompatible.
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
	builder := summaryBuilder{
		summary:     ExperimentSummary{Total: len(results)},
		assessments: make(map[AssessmentID]int),
		metrics:     make(map[observationKey]*metricSamples),
	}
	for _, result := range results {
		if err := builder.add(result); err != nil {
			return ExperimentSummary{}, err
		}
	}
	return builder.build(), nil
}

type summaryBuilder struct {
	summary     ExperimentSummary
	assessments map[AssessmentID]int
	metrics     map[observationKey]*metricSamples
}

type metricSamples struct {
	index        int
	scores       []float64
	measurements []float64
}

func (s *summaryBuilder) add(result CaseResult) error {
	s.countCase(result.Result)
	for _, assessment := range result.Result.Results {
		s.assessment(assessment.ID).count(assessment.Status())
		if assessment.Status() != AssessmentCompleted {
			continue
		}
		if err := s.observe(assessment.ID, *assessment.Report); err != nil {
			return fmt.Errorf("eval: summarize case %q assessment %q: %w", result.ID, assessment.ID, err)
		}
	}
	return nil
}

func (s *summaryBuilder) countCase(result SuiteResult) {
	if !result.Complete() {
		s.summary.Errors++
		if slices.ContainsFunc(result.Results, func(assessment AssessmentResult) bool {
			return assessment.Status() == AssessmentCompleted
		}) {
			s.summary.Partial++
		}
		return
	}
	s.summary.Evaluated++
	tallyVerdict(result.Verdict(), &s.summary.Passed, &s.summary.Failed, &s.summary.Unjudged)
}

func (s *summaryBuilder) assessment(id AssessmentID) *AssessmentSummary {
	index, exists := s.assessments[id]
	if !exists {
		index = len(s.summary.Assessments)
		s.assessments[id] = index
		s.summary.Assessments = append(s.summary.Assessments, AssessmentSummary{ID: id})
	}
	return &s.summary.Assessments[index]
}

func (s *summaryBuilder) observe(id AssessmentID, report Report) error {
	details := detailMetricsOf(report.Details)
	identity, err := observationIdentity(report.Metric, details)
	if err != nil {
		return err
	}
	key := observationKey{assessment: id, metric: identity}
	samples := s.metrics[key]
	if samples == nil {
		samples = &metricSamples{index: len(s.summary.Metrics)}
		s.metrics[key] = samples
		s.summary.Metrics = append(s.summary.Metrics, MetricSummary{AssessmentID: id, Metric: report.Metric, Details: details})
	}
	metric := &s.summary.Metrics[samples.index]
	metric.Evaluated++
	tallyVerdict(report.Verdict(), &metric.Passed, &metric.Failed, &metric.Unjudged)
	if report.Score != nil {
		samples.scores = append(samples.scores, report.Score.Float64())
	}
	if report.Measurement != nil {
		samples.measurements = append(samples.measurements, *report.Measurement)
	}
	return nil
}

func (s *summaryBuilder) build() ExperimentSummary {
	for _, samples := range s.metrics {
		metric := &s.summary.Metrics[samples.index]
		metric.Scores = distribution(samples.scores)
		metric.Measurements = distribution(samples.measurements)
	}
	return s.summary
}

func (a *AssessmentSummary) count(status AssessmentStatus) {
	switch status {
	case AssessmentCompleted:
		a.Completed++
	case AssessmentFailed:
		a.Failed++
	case AssessmentCanceled:
		a.Canceled++
	case AssessmentNotEvaluated:
		a.NotEvaluated++
	}
}

func tallyVerdict(verdict Verdict, passed, failed, unjudged *int) {
	switch verdict {
	case VerdictPass:
		*passed++
	case VerdictFail:
		*failed++
	default:
		*unjudged++
	}
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
