package eval_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

func TestDecisionValidatesAndOwnsItsWireIdentity(t *testing.T) {
	decision, err := eval.Score(0.8).Decide(0.8)
	if err != nil || decision.Policy != "threshold" || decision.Verdict != eval.VerdictPass || string(decision.Parameters["threshold"]) != "0.8" {
		t.Fatalf("threshold decision = %#v, %v", decision, err)
	}
	for _, candidate := range []eval.Decision{
		{Verdict: eval.VerdictPass},
		{Policy: " invalid", Verdict: eval.VerdictPass},
		{Policy: "test"},
		{Policy: "test", Verdict: "maybe"},
		{Policy: "test", Verdict: eval.VerdictPass, Parameters: metadata.Map{"broken": json.RawMessage(`{`)}},
	} {
		if validationErr := candidate.Validate(); !errors.Is(validationErr, eval.ErrInvalidReport) {
			t.Fatalf("invalid decision = %#v, %v", candidate, validationErr)
		}
		if _, marshalErr := jsonv2.Marshal(candidate); !errors.Is(marshalErr, eval.ErrInvalidReport) {
			t.Fatalf("encoded invalid decision = %#v, %v", candidate, marshalErr)
		}
	}
	report := eval.Report{Metric: testMetric("quality"), Decision: &decision}
	clone, err := report.Clone()
	if err != nil {
		t.Fatal(err)
	}
	clone.Decision.Verdict = eval.VerdictFail
	clone.Decision.Parameters["threshold"][2] = '9'
	if report.Verdict() != eval.VerdictPass || string(decision.Parameters["threshold"]) != "0.8" {
		t.Fatal("report clone aliases decision identity")
	}
	encoded, err := jsonv2.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded eval.Report
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, report) {
		t.Fatalf("decision round trip = %#v, %v", decoded, err)
	}
	for _, input := range []string{
		`{"metric":{"name":"quality"},"verdict":"pass"}`,
		`{"metric":{"name":"quality"},"decision":{"policy":"test","verdict":"pass","extra":true}}`,
		`{"metric":{"name":"quality"},"decision":{"policy":"test"}}`,
	} {
		if err := jsonv2.Unmarshal([]byte(input), &decoded); !errors.Is(err, eval.ErrInvalidReport) {
			t.Fatalf("accepted invalid decision wire: %s, %v", input, err)
		}
		if !reflect.DeepEqual(decoded, report) {
			t.Fatal("rejected decision decode changed receiver")
		}
	}
}

func TestCompositeAcceptsScoreOnlyAndKeepsPolicyOutOfCalculationIdentity(t *testing.T) {
	component := func(threshold *eval.Score) eval.Evaluator[string] {
		return eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
			score := eval.Score(0.6)
			report := eval.Report{Metric: testMetric("quality"), Score: &score}
			if threshold != nil {
				decision, err := score.Decide(*threshold)
				if err != nil {
					return eval.Report{}, err
				}
				report.Decision = &decision
			}
			return report, nil
		})
	}
	scoreOnly, err := eval.NewCompositeEvaluator(eval.CompositeEvaluatorConfig[string]{Components: []eval.Component[string]{{Evaluator: component(nil)}}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := scoreOnly.Evaluate(t.Context(), "input")
	if err != nil || report.Score == nil || *report.Score != 0.6 || report.Decision != nil || report.Verdict() != eval.VerdictUnspecified {
		t.Fatalf("score-only composite = %#v, %v", report, err)
	}
	var baseline eval.Report
	for _, threshold := range []eval.Score{0.5, 0.8} {
		composite, constructErr := eval.NewCompositeEvaluator(eval.CompositeEvaluatorConfig[string]{Components: []eval.Component[string]{{Evaluator: component(&threshold)}}, PassPolicy: eval.PassAll})
		if constructErr != nil {
			t.Fatal(constructErr)
		}
		decided, evaluateErr := composite.Evaluate(t.Context(), "input")
		if evaluateErr != nil || !reflect.DeepEqual(decided.Metric, report.Metric) || decided.Decision == nil {
			t.Fatalf("decision changed score identity: %#v, %v", decided, evaluateErr)
		}
		if threshold == 0.5 {
			baseline = decided
		} else if baseline.Verdict() != eval.VerdictPass || decided.Verdict() != eval.VerdictFail || !reflect.DeepEqual(baseline.Decision.Parameters, decided.Decision.Parameters) {
			t.Fatalf("component thresholds leaked into the composite's own rule: %#v, %#v", baseline, decided)
		}
	}
	explicit, err := eval.NewCompositeEvaluator(eval.CompositeEvaluatorConfig[string]{Components: []eval.Component[string]{{Evaluator: component(nil)}}, PassPolicy: eval.PassAll})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := explicit.Evaluate(t.Context(), "input"); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("categorical policy accepted undecided component: %v", err)
	}
	if _, err := eval.NewCompositeEvaluator(eval.CompositeEvaluatorConfig[string]{Components: []eval.Component[string]{{Evaluator: component(nil), Required: true}}}); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("required gate without categorical policy = %v", err)
	}
}
