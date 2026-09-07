package ranking_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/ranking"
)

func TestEvaluatorCalculatesRankingMetricsAtCutoff(t *testing.T) {
	sample, err := ranking.NewSample(
		[]string{"a", "b", "c", "d"},
		[]ranking.Judgment{
			{Identity: "b", Relevance: 3},
			{Identity: "d", Relevance: 1},
			{Identity: "e", Relevance: 2},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		metric ranking.Metric
		want   float64
	}{
		{metric: ranking.MetricPrecision, want: 0.5},
		{metric: ranking.MetricRecall, want: 2.0 / 3.0},
		{metric: ranking.MetricReciprocalRank, want: 0.5},
		{metric: ranking.MetricAveragePrecision, want: 1.0 / 3.0},
		{
			metric: ranking.MetricNDCG,
			want: (3/math.Log2(3) + 1/math.Log2(5)) /
				(3 + 2/math.Log2(3) + 1/math.Log2(4)),
		},
	}

	for _, test := range tests {
		t.Run(string(test.metric), func(t *testing.T) {
			threshold := eval.Score(0.6)
			evaluator, err := ranking.NewEvaluator(ranking.Config{
				Metric: test.metric, Cutoff: 4, Threshold: &threshold,
			})
			if err != nil {
				t.Fatal(err)
			}
			report, err := evaluator.Evaluate(t.Context(), sample)
			if err != nil {
				t.Fatal(err)
			}
			if report.Score == nil || math.Abs(report.Score.Float64()-test.want) > 1e-12 {
				t.Fatalf("score = %v, want %v", report.Score, test.want)
			}
			if report.Metric.Namespace() != "ranking" || report.Metric.Name() != eval.MetricName(test.metric) {
				t.Fatalf("metric = %#v", report.Metric)
			}
			cutoff, found, err := report.Metric.Parameters().Decode[int]("cutoff")
			if err != nil || !found || cutoff != 4 {
				t.Fatalf("metric cutoff = (%d, %v, %v)", cutoff, found, err)
			}
			gotThreshold, found, err := report.Metric.Parameters().Decode[eval.Score]("threshold")
			if err != nil || !found || gotThreshold != threshold {
				t.Fatalf("metric threshold = (%v, %v, %v)", gotThreshold, found, err)
			}
			wantVerdict := eval.VerdictFail
			if test.want >= threshold.Float64() {
				wantVerdict = eval.VerdictPass
			}
			if report.Verdict != wantVerdict {
				t.Fatalf("verdict = %v for score %v and threshold %v", report.Verdict, report.Score, threshold)
			}
		})
	}
}

func TestPrecisionUsesConfiguredCutoff(t *testing.T) {
	evaluator, err := ranking.NewEvaluator(ranking.Config{Metric: ranking.MetricPrecision, Cutoff: 4})
	if err != nil {
		t.Fatal(err)
	}
	sample, err := ranking.NewSample(
		[]string{"relevant"},
		[]ranking.Judgment{{Identity: "relevant", Relevance: 1}},
	)
	if err != nil {
		t.Fatal(err)
	}
	report, err := evaluator.Evaluate(t.Context(), sample)
	if err != nil {
		t.Fatal(err)
	}
	if report.Score == nil || *report.Score != 0.25 {
		t.Fatalf("precision@4 = %v, want 0.25", report.Score)
	}
	if report.Verdict != eval.VerdictUnspecified {
		t.Fatalf("verdict = %q without a configured threshold", report.Verdict)
	}
}

func TestNDCGIsInvariantToGradeScale(t *testing.T) {
	evaluator, err := ranking.NewEvaluator(ranking.Config{Metric: ranking.MetricNDCG, Cutoff: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, scale := range []struct {
		name  string
		grade float64
	}{
		{name: "unit", grade: 1},
		{name: "largest finite", grade: math.MaxFloat64},
		{name: "smallest positive", grade: math.SmallestNonzeroFloat64},
	} {
		for _, test := range []struct {
			name      string
			ranking   []string
			judgments []ranking.Judgment
			want      float64
		}{
			{
				name: "perfect", ranking: []string{"a", "b"},
				judgments: []ranking.Judgment{{Identity: "a", Relevance: scale.grade}, {Identity: "b", Relevance: scale.grade}},
				want:      1,
			},
			{
				name: "second rank", ranking: []string{"missing", "a"},
				judgments: []ranking.Judgment{{Identity: "a", Relevance: scale.grade}},
				want:      0.6309297535714574,
			},
		} {
			t.Run(scale.name+"/"+test.name, func(t *testing.T) {
				sample, sampleErr := ranking.NewSample(test.ranking, test.judgments)
				if sampleErr != nil {
					t.Fatal(sampleErr)
				}
				report, evaluationErr := evaluator.Evaluate(t.Context(), sample)
				if evaluationErr != nil {
					t.Fatal(evaluationErr)
				}
				if report.Score == nil || math.Abs(report.Score.Float64()-test.want) > 1e-12 {
					t.Fatalf("NDCG=%v, want %v", report.Score, test.want)
				}
			})
		}
	}
}

func TestSampleOwnsAndValidatesRanking(t *testing.T) {
	identities := []string{"a"}
	judgments := []ranking.Judgment{{Identity: "a", Relevance: 1}}
	sample, err := ranking.NewSample(identities, judgments)
	if err != nil {
		t.Fatal(err)
	}
	identities[0], judgments[0].Identity = "changed", "changed"
	clone := sample.Clone()
	clone.Ranking[0] = "clone"
	clone.Judgments[0].Identity = "clone"
	if sample.Ranking[0] != "a" || sample.Judgments[0].Identity != "a" {
		t.Fatalf("sample aliases caller or clone: %#v", sample)
	}

	invalid := []ranking.Sample{
		{},
		{Ranking: []string{" "}, Judgments: []ranking.Judgment{{Identity: "a", Relevance: 1}}},
		{Ranking: []string{"a", "a"}, Judgments: []ranking.Judgment{{Identity: "a", Relevance: 1}}},
		{Ranking: []string{"a"}, Judgments: []ranking.Judgment{{Identity: "a"}}},
		{Ranking: []string{"a"}, Judgments: []ranking.Judgment{{Identity: "a", Relevance: -1}}},
		{Ranking: []string{"a"}, Judgments: []ranking.Judgment{{Identity: "a", Relevance: math.NaN()}}},
		{Ranking: []string{"a"}, Judgments: []ranking.Judgment{{Identity: "a", Relevance: 1}, {Identity: "a", Relevance: 2}}},
	}
	for _, candidate := range invalid {
		if err := candidate.Validate(); !errors.Is(err, ranking.ErrInvalidSample) {
			t.Fatalf("Validate(%#v) error = %v", candidate, err)
		}
	}
}

func TestEvaluatorValidatesConfigAndCancellation(t *testing.T) {
	if _, err := ranking.NewEvaluator(ranking.Config{Cutoff: 1}); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("missing metric error = %v", err)
	}
	if _, err := ranking.NewEvaluator(ranking.Config{Metric: ranking.MetricRecall}); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("invalid cutoff error = %v", err)
	}
	invalidThreshold := eval.Score(2)
	if _, err := ranking.NewEvaluator(ranking.Config{
		Metric: ranking.MetricRecall, Cutoff: 1, Threshold: &invalidThreshold,
	}); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("invalid threshold error = %v", err)
	}

	evaluator, err := ranking.NewEvaluator(ranking.Config{Metric: ranking.MetricRecall, Cutoff: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := evaluator.Evaluate(ctx, ranking.Sample{
		Judgments: []ranking.Judgment{{Identity: "a", Relevance: 1}},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}
