package main

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"maps"
	"math"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "objective: improve the release draft\n" +
		"best: draft 3; addressed: raise quality after revision 2\n" +
		"score: 0.95\n" +
		"attempts: 3\n" +
		"accepted: true\n" +
		"processes: 10\n"
	if output.String() != want {
		t.Fatalf("output=%q, want %q", output.String(), want)
	}
}

func TestExhaustionReturnsBestAttemptNotLatestAttempt(t *testing.T) {
	report, evidence, err := execute(
		context.Background(),
		optimizationRequest{Objective: "retain the best revision"},
		[]float64{0.5, 0.9, 0.3},
		0.95,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Accepted || len(report.History) != 3 || evidence.ProcessCount != 10 {
		t.Fatalf("report=%#v evidence=%#v", report, evidence)
	}
	if report.best() != report.History[1] || report.best().Assessment.Score != 0.9 {
		t.Fatalf("best=%#v, want revision 2 at 0.9", report.best())
	}
	if !strings.Contains(report.History[1].Candidate.Content, report.History[0].Assessment.Feedback) {
		t.Fatalf("second candidate did not consume first feedback: %#v", report.History)
	}
	if report.History[2].Candidate == report.best().Candidate {
		t.Fatal("latest lower-scoring candidate replaced the stable best attempt")
	}
	wantDeployments := expectedDeployments(3)
	if !maps.Equal(evidence.Deployments, wantDeployments) {
		t.Fatalf("deployments=%#v, want %#v", evidence.Deployments, wantDeployments)
	}
}

func TestAcceptedAttemptStopsBeforeLimit(t *testing.T) {
	report, evidence, err := execute(
		context.Background(),
		optimizationRequest{Objective: "stop at the first accepted revision"},
		[]float64{0.95, 0.2, 0.1},
		0.9,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Accepted || len(report.History) != 1 {
		t.Fatalf("report=%#v, want one accepted iteration", report)
	}
	wantDeployments := expectedDeployments(1)
	if evidence.ProcessCount != 4 || !maps.Equal(evidence.Deployments, wantDeployments) {
		t.Fatalf("evidence=%#v, want exact root/body/optimizer/evaluator tree", evidence)
	}
}

func expectedDeployments(iterations int) map[string]int {
	return map[string]int{
		"example.evaluator_optimizer":           1,
		"example.evaluator_optimizer.iteration": iterations,
		"example.evaluator_optimizer.optimizer": iterations,
		"example.evaluator_optimizer.evaluator": iterations,
	}
}

func TestEqualScoresKeepEarliestAttempt(t *testing.T) {
	report, _, err := execute(
		context.Background(),
		optimizationRequest{Objective: "keep deterministic ties"},
		[]float64{0.8, 0.8, 0.7},
		0.95,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.best() != report.History[0] || report.best().Assessment.Score != 0.8 {
		t.Fatalf("best=%#v, want earliest tied revision", report.best())
	}
}

func TestConfigurationIsExplicitAndFinite(t *testing.T) {
	tests := []struct {
		name          string
		scores        []float64
		threshold     float64
		maxIterations uint32
	}{
		{name: "zero iterations", scores: []float64{1}, threshold: 1},
		{name: "short schedule", scores: []float64{0.5}, threshold: 0.9, maxIterations: 2},
		{name: "zero threshold", scores: []float64{0.5}, maxIterations: 1},
		{name: "nan threshold", scores: []float64{0.5}, threshold: math.NaN(), maxIterations: 1},
		{name: "infinite score", scores: []float64{math.Inf(1)}, threshold: 0.9, maxIterations: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newEvaluatorOptimizer(test.scores, test.threshold, uint64(test.maxIterations)); err == nil {
				t.Fatal("invalid evaluator-optimizer configuration was accepted")
			}
		})
	}
}

func TestOptimizationHistoryOwnsEvaluatedCandidates(t *testing.T) {
	state := optimizationState{Objective: "one history", History: []attempt{
		{Candidate: candidate{Content: "draft"}, Assessment: assessment{Score: 0.95, Feedback: "accepted"}},
	}}
	if err := state.validateSettled(); err != nil {
		t.Fatal(err)
	}
	data, err := jsonv2.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"objective":"one history","history":[{"candidate":{"content":"draft"},"assessment":{"score":0.95,"feedback":"accepted"}}]}`
	if string(data) != want {
		t.Fatalf("evaluated state duplicates history: %s", data)
	}
	for _, member := range []string{`"best":{}`, `"has_best":true`, `"accepted":true`} {
		legacy := strings.TrimSuffix(want, "}") + "," + member + "}"
		payload, err := agent.ParsePayload([]byte(legacy))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := payload.Decode[optimizationState](); !errors.Is(err, jsonv2.ErrUnknownName) {
			t.Fatalf("accepted second owner %s: %v", member, err)
		}
	}
	state.Current = &state.History[0].Candidate
	if err := state.validateSettled(); err == nil {
		t.Fatal("evaluated candidate remained independently writable")
	}
}
