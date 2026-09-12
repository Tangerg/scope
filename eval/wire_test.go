package eval_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

func accuracyMetric(t *testing.T) eval.Metric {
	t.Helper()
	metric, err := eval.NewMetric(eval.MetricConfig{
		Namespace: "quality",
		Name:      "accuracy",
		Direction: eval.DirectionHigherIsBetter,
	})
	if err != nil {
		t.Fatal(err)
	}
	return metric
}

// TestReportSurvivesJSON is the contract a Host relies on when it persists a
// result: a report has to come back validated, with its child tree intact.
func TestReportSurvivesJSON(t *testing.T) {
	score := eval.Score(0.75)
	measurement := 12.5
	report := eval.Report{
		Metric:      accuracyMetric(t),
		Score:       &score,
		Measurement: &measurement,
		Feedback:    "close enough",
		Metadata:    metadata.Map{"run": json.RawMessage(`"1"`)},
		Details: []eval.Report{
			{Metric: accuracyMetric(t), Feedback: "child"},
		},
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}

	var decoded eval.Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Score == nil || *decoded.Score != score {
		t.Fatalf("score round trip = %v", decoded.Score)
	}
	if decoded.Measurement == nil || *decoded.Measurement != measurement {
		t.Fatalf("measurement round trip = %v", decoded.Measurement)
	}
	if decoded.Feedback != "close enough" || len(decoded.Details) != 1 {
		t.Fatalf("report round trip = %#v", decoded)
	}
	if decoded.Details[0].Feedback != "child" {
		t.Fatalf("child report round trip = %#v", decoded.Details[0])
	}
}

func TestReportJSONRejectsUnknownMembers(t *testing.T) {
	for name, data := range map[string]string{
		"report": `{"metric":{"name":"accuracy"},"feedback":"accepted","unexpected":true}`,
		"metric": `{"metric":{"name":"accuracy","unexpected":true},"feedback":"accepted"}`,
		"detail": `{"metric":{"name":"accuracy"},"details":[{"metric":{"name":"accuracy"},"feedback":"child","unexpected":true}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			kept := eval.Report{Metric: accuracyMetric(t), Feedback: "kept"}
			if err := json.Unmarshal([]byte(data), &kept); !errors.Is(err, jsonv2.ErrUnknownName) {
				t.Fatalf("decode error = %v, want unknown object member", err)
			}
			if kept.Feedback != "kept" || len(kept.Details) != 0 || kept.Metric.Namespace() != "quality" {
				t.Fatalf("rejected decode changed report: %+v", kept)
			}
		})
	}
}

// TestAbsentMeasurementsStayAbsent proves the pointer fields carry presence:
// an evaluation that produced no score must not encode one, or a consumer would
// read a zero score as a real judgment.
func TestAbsentMeasurementsStayAbsent(t *testing.T) {
	qualitative := eval.Report{Metric: accuracyMetric(t), Feedback: "reads well"}
	encoded, err := json.Marshal(qualitative)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"score", "measurement", "verdict", "details"} {
		if _, present := wire[field]; present {
			t.Errorf("unset %s was encoded as %v", field, wire[field])
		}
	}
}

// TestReportDecodeIsValidatedBeforeAssignment keeps a malformed persisted
// report from replacing a good one in memory.
func TestReportDecodeIsValidatedBeforeAssignment(t *testing.T) {
	if err := (*eval.Report)(nil).UnmarshalJSON([]byte(`{}`)); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("nil receiver error = %v", err)
	}

	kept := eval.Report{Metric: accuracyMetric(t), Feedback: "kept"}
	if err := kept.UnmarshalJSON([]byte(`{`)); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("malformed decode error = %v", err)
	}
	if err := kept.UnmarshalJSON([]byte(`{"metric":{"name":""}}`)); err == nil {
		t.Fatal("a report without a metric name decoded successfully")
	}
	if kept.Feedback != "kept" {
		t.Fatalf("a failed decode mutated the receiver: %#v", kept)
	}
}

// TestReportMarshalRefusesAnInvalidReport keeps a report that would fail
// validation from reaching a store, where it would only fail on the way back.
func TestReportMarshalRefusesAnInvalidReport(t *testing.T) {
	if _, err := json.Marshal(eval.Report{}); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("Marshal error = %v", err)
	}
}

// TestReportDepthIsBoundedAtEveryBoundary is the invariant that keeps a deeply
// nested or cyclic-looking tree from being accepted at one boundary and
// rejected at another.
func TestReportDepthIsBoundedAtEveryBoundary(t *testing.T) {
	metric := accuracyMetric(t)
	deep := eval.Report{Metric: metric}
	for range eval.MaxReportDepth + 1 {
		deep = eval.Report{Metric: metric, Details: []eval.Report{deep}}
	}

	if err := deep.Validate(); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("Validate error = %v", err)
	}
	if _, err := deep.Clone(); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("Clone error = %v", err)
	}
	if _, err := json.Marshal(deep); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("Marshal error = %v", err)
	}
}

// TestMetricIdentityReadsAsANamespacedName is what a caller sees in a report
// and a metric dimension, so it has to stay stable and unambiguous.
func TestMetricIdentityReadsAsANamespacedName(t *testing.T) {
	namespaced := accuracyMetric(t)
	if namespaced.String() != "quality/accuracy" {
		t.Fatalf("String = %q", namespaced.String())
	}

	bare, err := eval.NewMetric(eval.MetricConfig{Name: "accuracy"})
	if err != nil {
		t.Fatal(err)
	}
	if bare.String() != "accuracy" {
		t.Fatalf("String = %q, want no separator without a namespace", bare.String())
	}
}

func TestDirectionIsAClosedVocabulary(t *testing.T) {
	for _, direction := range []eval.Direction{
		eval.DirectionUnspecified,
		eval.DirectionHigherIsBetter,
		eval.DirectionLowerIsBetter,
	} {
		if err := direction.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v", direction, err)
		}
	}
	if err := (eval.Direction("sideways")).Validate(); !errors.Is(err, eval.ErrInvalidMetric) {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestMetricCopiesPreserveParameterIsolation(t *testing.T) {
	metric, err := eval.NewMetric(eval.MetricConfig{
		Name:       "accuracy",
		Parameters: metadata.Map{"threshold": json.RawMessage(`0.5`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	copied := metric
	parameters := copied.Parameters()
	parameters["threshold"][2] = '9'
	if got := string(copied.Parameters()["threshold"]); got != "0.5" {
		t.Fatalf("copied metric threshold = %s, want 0.5", got)
	}
	if err := json.Unmarshal([]byte(`{"name":"accuracy","parameters":{"threshold":0.9}}`), &copied); err != nil {
		t.Fatal(err)
	}
	if got := string(copied.Parameters()["threshold"]); got != "0.9" {
		t.Fatalf("decoded metric threshold = %s, want 0.9", got)
	}
	if got := string(metric.Parameters()["threshold"]); got != "0.5" {
		t.Fatalf("original metric threshold = %s, want 0.5", got)
	}
}

// TestCaseValidationCoversItsWholeIdentity keeps a dataset from accepting a
// case whose metadata cannot be persisted alongside its result.
func TestCaseValidationCoversItsWholeIdentity(t *testing.T) {
	if err := (eval.Case[string]{}).Validate(); err == nil {
		t.Fatal("a case without an ID validated")
	}
	broken := eval.Case[string]{
		ID:       "one",
		Metadata: metadata.Map{"bad": json.RawMessage(`{`)},
	}
	if err := broken.Validate(); !errors.Is(err, eval.ErrInvalidCase) {
		t.Fatalf("Validate error = %v", err)
	}
}

// TestDatasetOwnsItsCases is the ownership rule: a caller must not be able to
// change a dataset after construction, or an experiment would not be repeatable.
func TestDatasetOwnsItsCases(t *testing.T) {
	original := metadata.Map{"tag": json.RawMessage(`"a"`)}
	dataset, err := eval.NewDataset("test-fixture", eval.Case[string]{ID: "one", Subject: "s", Metadata: original})
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Len() != 1 {
		t.Fatalf("Len = %d, want 1", dataset.Len())
	}

	original["tag"] = json.RawMessage(`"b"`)
	cases := dataset.Cases()
	if string(cases[0].Metadata["tag"]) != `"a"` {
		t.Fatal("NewDataset aliases the caller's metadata")
	}

	cases[0].Metadata["tag"] = json.RawMessage(`"c"`)
	if string(dataset.Cases()[0].Metadata["tag"]) != `"a"` {
		t.Fatal("Cases hands out an alias to dataset state")
	}
}

func TestNewDatasetRejectsDuplicateIdentities(t *testing.T) {
	_, err := eval.NewDataset("test-fixture",
		eval.Case[string]{ID: "one", Subject: "a"},
		eval.Case[string]{ID: "one", Subject: "b"},
	)
	if err == nil {
		t.Fatal("NewDataset accepted a duplicate case ID")
	}
}

type constantEvaluator struct{ report eval.Report }

func (c constantEvaluator) Evaluate(context.Context, string) (eval.Report, error) {
	return c.report, nil
}

// TestProjectionEvaluatorAdaptsAndReportsProjectionFailure is the whole point
// of a projection: an aggregate subject reaches a narrow evaluator, and a
// projection that cannot produce one fails loudly instead of evaluating a zero
// value.
func TestProjectionEvaluatorAdaptsAndReportsProjectionFailure(t *testing.T) {
	inner := constantEvaluator{report: eval.Report{Metric: accuracyMetric(t), Feedback: "ok"}}

	evaluator, err := eval.NewProjectionEvaluator(inner, func(value int) (string, error) {
		return strings.Repeat("x", value), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := evaluator.Evaluate(t.Context(), 3)
	if err != nil || report.Feedback != "ok" {
		t.Fatalf("Evaluate = %#v, %v", report, err)
	}

	boom := errors.New("cannot project")
	failing, err := eval.NewProjectionEvaluator(inner, func(int) (string, error) { return "", boom })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Evaluate(t.Context(), 1); !errors.Is(err, boom) {
		t.Fatalf("Evaluate error = %v, want %v", err, boom)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := evaluator.Evaluate(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Evaluate error = %v, want context.Canceled", err)
	}
}

func TestNewProjectionEvaluatorRejectsAnIncompleteConfig(t *testing.T) {
	if _, err := eval.NewProjectionEvaluator[int, string](nil, func(int) (string, error) { return "", nil }); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("nil evaluator error = %v", err)
	}
	if _, err := eval.NewProjectionEvaluator[int, string](constantEvaluator{}, nil); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("nil projection error = %v", err)
	}
}
