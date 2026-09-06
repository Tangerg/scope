// Package ranking evaluates ranked outputs against graded relevance judgments.
package ranking

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

const (
	metricNamespace    = "ranking"
	metricCutoffKey    = "cutoff"
	metricThresholdKey = "threshold"
)

// ErrInvalidSample identifies a ranking or judgment set that cannot define one
// deterministic relevance calculation.
var ErrInvalidSample = errors.New("eval/ranking: invalid sample")

// Metric selects a ranking-quality calculation evaluated at a configured
// cutoff.
type Metric string

const (
	// MetricPrecision measures relevant results among the admitted ranking.
	MetricPrecision Metric = "precision"
	// MetricRecall measures admitted relevant results among all relevant items.
	MetricRecall Metric = "recall"
	// MetricReciprocalRank rewards the rank of the first relevant result.
	MetricReciprocalRank Metric = "reciprocal_rank"
	// MetricAveragePrecision averages precision at every relevant result.
	MetricAveragePrecision Metric = "average_precision"
	// MetricNDCG preserves graded relevance while discounting later ranks.
	MetricNDCG Metric = "ndcg"
)

const (
	rankingField  = "ranking"
	judgmentField = "judgments"
)

func (m Metric) Validate() error {
	switch m {
	case MetricPrecision, MetricRecall, MetricReciprocalRank, MetricAveragePrecision, MetricNDCG:
		return nil
	default:
		return fmt.Errorf("%w: unsupported ranking metric %q", eval.ErrInvalidMetric, m)
	}
}

func (m Metric) reportMetric(cutoff int, threshold *eval.Score) (eval.Metric, error) {
	parameters := metadata.Map{}
	if err := parameters.Set(metricCutoffKey, cutoff); err != nil {
		return eval.Metric{}, err
	}
	if threshold != nil {
		if err := parameters.Set(metricThresholdKey, threshold); err != nil {
			return eval.Metric{}, err
		}
	}
	return eval.NewMetric(eval.MetricConfig{
		Namespace: metricNamespace, Name: eval.MetricName(m), Parameters: parameters,
	})
}

// Judgment assigns a non-negative relevance grade to one ranked identity. A
// positive grade is relevant for binary metrics; NDCG uses the full grade.
type Judgment struct {
	Identity  string  `json:"identity"`
	Relevance float64 `json:"relevance"`
}

// Sample contains one observed ranking and its relevance judgments. Ranking
// identities without a judgment are treated as having zero relevance.
type Sample struct {
	Ranking   []string   `json:"ranking,omitzero"`
	Judgments []Judgment `json:"judgments"`
}

// NewSample snapshots identities and judgments before validating their unique
// correlation.
func NewSample(ranking []string, judgments []Judgment) (Sample, error) {
	sample := Sample{Ranking: slices.Clone(ranking), Judgments: slices.Clone(judgments)}
	if err := sample.Validate(); err != nil {
		return Sample{}, err
	}
	return sample, nil
}

func (s Sample) Clone() Sample {
	s.Ranking = slices.Clone(s.Ranking)
	s.Judgments = slices.Clone(s.Judgments)
	return s
}

func (s Sample) Validate() error {
	if err := validateIdentities(rankingField, s.Ranking); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(s.Judgments))
	hasRelevant := false
	for index, judgment := range s.Judgments {
		if err := validateIdentity(fmt.Sprintf("%s[%d].identity", judgmentField, index), judgment.Identity); err != nil {
			return err
		}
		if _, exists := seen[judgment.Identity]; exists {
			return fmt.Errorf("%w: %s contains duplicate identity %q", ErrInvalidSample, judgmentField, judgment.Identity)
		}
		seen[judgment.Identity] = struct{}{}
		if math.IsNaN(judgment.Relevance) || math.IsInf(judgment.Relevance, 0) || judgment.Relevance < 0 {
			return fmt.Errorf("%w: %s[%d].relevance must be finite and non-negative", ErrInvalidSample, judgmentField, index)
		}
		hasRelevant = hasRelevant || judgment.Relevance > 0
	}
	if !hasRelevant {
		return fmt.Errorf("%w: at least one positive relevance judgment is required", ErrInvalidSample)
	}
	return nil
}

func validateIdentities(label string, identities []string) error {
	seen := make(map[string]struct{}, len(identities))
	for index, identity := range identities {
		if err := validateIdentity(fmt.Sprintf("%s[%d]", label, index), identity); err != nil {
			return err
		}
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("%w: %s contains duplicate identity %q", ErrInvalidSample, label, identity)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func validateIdentity(label, identity string) error {
	if identity == "" || identity != strings.TrimSpace(identity) {
		return fmt.Errorf("%w: %s must be non-empty without surrounding whitespace", ErrInvalidSample, label)
	}
	return nil
}

type relevanceSet map[string]float64

func newRelevanceSet(judgments []Judgment) relevanceSet {
	relevance := make(relevanceSet, len(judgments))
	for _, judgment := range judgments {
		relevance[judgment.Identity] = judgment.Relevance
	}
	return relevance
}

func (r relevanceSet) relevantCount() int {
	count := 0
	for _, grade := range r {
		if grade > 0 {
			count++
		}
	}
	return count
}

func (r relevanceSet) count(ranking []string) int {
	count := 0
	for _, identity := range ranking {
		if r[identity] > 0 {
			count++
		}
	}
	return count
}

func (r relevanceSet) precisionAt(ranking []string, cutoff int) float64 {
	return float64(r.count(ranking)) / float64(cutoff)
}

func (r relevanceSet) recallAt(ranking []string) float64 {
	return float64(r.count(ranking)) / float64(r.relevantCount())
}

func (r relevanceSet) reciprocalRank(ranking []string) float64 {
	for index, identity := range ranking {
		if r[identity] > 0 {
			return 1 / float64(index+1)
		}
	}
	return 0
}

func (r relevanceSet) averagePrecisionAt(ranking []string, cutoff int) float64 {
	hits := 0
	total := 0.0
	for index, identity := range ranking {
		if r[identity] <= 0 {
			continue
		}
		hits++
		total += float64(hits) / float64(index+1)
	}
	return total / float64(min(r.relevantCount(), cutoff))
}

func discountedGain(relevance float64, rank int) float64 {
	return relevance / math.Log2(float64(rank+1))
}

func (r relevanceSet) ndcgAt(ranking []string, cutoff int) float64 {
	ideal := make([]float64, 0, len(r))
	for _, grade := range r {
		if grade > 0 {
			ideal = append(ideal, grade)
		}
	}
	slices.SortFunc(ideal, func(left, right float64) int {
		if left > right {
			return -1
		}
		if left < right {
			return 1
		}
		return 0
	})
	// NDCG is scale invariant. Normalize before discounting or summing so
	// finite grades cannot overflow or lose their ratio through underflow.
	scale := ideal[0]
	dcg := 0.0
	for index, identity := range ranking {
		dcg += discountedGain(r[identity]/scale, index+1)
	}
	idcg := 0.0
	for index, grade := range ideal[:min(cutoff, len(ideal))] {
		idcg += discountedGain(grade/scale, index+1)
	}
	return dcg / idcg
}

func (m Metric) score(ranking []string, relevance relevanceSet, cutoff int) float64 {
	switch m {
	case MetricPrecision:
		return relevance.precisionAt(ranking, cutoff)
	case MetricRecall:
		return relevance.recallAt(ranking)
	case MetricReciprocalRank:
		return relevance.reciprocalRank(ranking)
	case MetricAveragePrecision:
		return relevance.averagePrecisionAt(ranking, cutoff)
	case MetricNDCG:
		return relevance.ndcgAt(ranking, cutoff)
	default:
		return 0
	}
}

// Config configures one deterministic ranking evaluator. Cutoff is required
// and positive. A nil Threshold produces a score without a pass/fail verdict.
type Config struct {
	Metric    Metric
	Cutoff    int
	Threshold *eval.Score
}

func (c Config) validate() error {
	if err := c.Metric.Validate(); err != nil {
		return fmt.Errorf("%w: metric: %w", eval.ErrInvalidEvaluatorConfig, err)
	}
	if c.Cutoff <= 0 {
		return fmt.Errorf("%w: cutoff must be positive", eval.ErrInvalidEvaluatorConfig)
	}
	return nil
}

func (c Config) threshold() (*eval.Score, error) {
	if c.Threshold == nil {
		return nil, nil
	}
	threshold := *c.Threshold
	if err := threshold.Validate(); err != nil {
		return nil, fmt.Errorf("%w: threshold: %w", eval.ErrInvalidEvaluatorConfig, err)
	}
	return &threshold, nil
}

// Evaluator measures one standard ranking metric at a fixed cutoff.
type Evaluator struct {
	metric       Metric
	cutoff       int
	threshold    *eval.Score
	reportMetric eval.Metric
}

// NewEvaluator freezes the metric, cutoff, and optional decision threshold.
func NewEvaluator(config Config) (*Evaluator, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	threshold, err := config.threshold()
	if err != nil {
		return nil, err
	}
	reportMetric, err := config.Metric.reportMetric(config.Cutoff, threshold)
	if err != nil {
		return nil, fmt.Errorf("%w: report metric: %w", eval.ErrInvalidEvaluatorConfig, err)
	}
	return &Evaluator{
		metric: config.Metric, cutoff: config.Cutoff, threshold: threshold, reportMetric: reportMetric,
	}, nil
}

func (e *Evaluator) Evaluate(ctx context.Context, sample Sample) (eval.Report, error) {
	if err := ctx.Err(); err != nil {
		return eval.Report{}, err
	}
	if err := sample.Validate(); err != nil {
		return eval.Report{}, err
	}

	relevance := newRelevanceSet(sample.Judgments)
	ranking := sample.Ranking[:min(e.cutoff, len(sample.Ranking))]
	value := e.metric.score(ranking, relevance, e.cutoff)
	score, err := eval.NewScore(value)
	if err != nil {
		return eval.Report{}, fmt.Errorf("eval/ranking: calculate %s: %w", e.reportMetric, err)
	}
	verdict := eval.VerdictUnspecified
	if e.threshold != nil {
		verdict, err = score.Verdict(*e.threshold)
		if err != nil {
			return eval.Report{}, fmt.Errorf("eval/ranking: verdict: %w", err)
		}
	}
	report := eval.Report{Metric: e.reportMetric.Clone(), Verdict: verdict, Score: &score}
	if err := report.Validate(); err != nil {
		return eval.Report{}, err
	}
	return report, nil
}

var _ eval.Evaluator[Sample] = (*Evaluator)(nil)
