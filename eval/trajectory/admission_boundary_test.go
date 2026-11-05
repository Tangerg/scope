package trajectory_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestExpectationRejectsInvalidAssertionsBeforeEvaluation(t *testing.T) {
	invalidArguments := trajectory.ToolArguments(`{"value":`)
	for _, expected := range []trajectory.Expectation{
		{},
		{Status: run.StatusFailed, Output: recordedOutput(t, runTrajectory(t))},
		{Status: run.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: " "}}}},
		{Status: run.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: "tool", Arguments: &invalidArguments}}}},
		{Status: run.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: "tool", Outcome: "unsupported"}}}},
		{Status: run.StatusCompleted, Baseline: new(trajectory.Trajectory)},
		{Status: run.StatusCompleted, Limits: trajectory.Limits{TotalTokens: new(int64(-1))}},
		{Status: run.StatusCompleted, Limits: trajectory.Limits{Elapsed: new(-time.Nanosecond)}},
	} {
		if err := expected.Validate(); !errors.Is(err, trajectory.ErrInvalidSample) {
			t.Errorf("invalid expectation admitted: %+v, %v", expected, err)
		}
	}
}

func TestTrajectoryProjectionFailuresRemainErrors(t *testing.T) {
	actual := runTrajectory(t)
	projectionErr := errors.New("output projection unavailable")
	for _, test := range []struct {
		name       string
		projection func(run.Payload) (json.RawMessage, error)
		want       error
	}{
		{"projection failure", func(run.Payload) (json.RawMessage, error) { return nil, projectionErr }, projectionErr},
		{"invalid JSON", func(run.Payload) (json.RawMessage, error) { return json.RawMessage(`{"value":`), nil }, trajectory.ErrInvalidSample},
		{"duplicate member", func(run.Payload) (json.RawMessage, error) { return json.RawMessage(`{"value":1,"value":2}`), nil }, trajectory.ErrInvalidSample},
	} {
		t.Run(test.name, func(t *testing.T) {
			report, err := (trajectory.Evaluator{OutputProjection: test.projection}).Evaluate(t.Context(), trajectory.Sample{
				Actual: actual, Expected: trajectory.Expectation{Status: run.StatusCompleted, Baseline: &actual},
			})
			if !errors.Is(err, test.want) || report.Metric.Name() != "" {
				t.Fatalf("projection failure became a verdict: %+v, %v", report, err)
			}
		})
	}
}

func TestLimitsRejectUnknownWireMembersAtomically(t *testing.T) {
	limits := trajectory.Limits{TotalTokens: new(int64(15)), Elapsed: new(time.Minute)}
	if err := jsonv2.Unmarshal([]byte(`{"total_tokens":30,"other":1}`), &limits); !errors.Is(err, jsonv2.ErrUnknownName) {
		t.Fatalf("unknown limit admitted: %v", err)
	}
	if limits.TotalTokens == nil || *limits.TotalTokens != 15 || limits.Elapsed == nil || *limits.Elapsed != time.Minute {
		t.Fatalf("failed decode partially replaced limits: %+v", limits)
	}
}

func TestLimitsRejectNegativeWireBoundsAtomically(t *testing.T) {
	for _, data := range []string{`{"total_tokens":-1}`, `{"elapsed_ns":-1}`} {
		t.Run(data, func(t *testing.T) {
			limits := trajectory.Limits{TotalTokens: new(int64(15)), Elapsed: new(time.Minute)}
			if err := jsonv2.Unmarshal([]byte(data), &limits); !errors.Is(err, trajectory.ErrInvalidSample) {
				t.Fatalf("negative limit admitted: %v", err)
			}
			if limits.TotalTokens == nil || *limits.TotalTokens != 15 || limits.Elapsed == nil || *limits.Elapsed != time.Minute {
				t.Fatalf("failed decode partially replaced limits: %+v", limits)
			}
		})
	}
}

func TestLimitsRejectNegativeBoundsOnMarshal(t *testing.T) {
	for _, limits := range []trajectory.Limits{
		{TotalTokens: new(int64(-1))},
		{Elapsed: new(time.Duration(-1))},
	} {
		if _, err := jsonv2.Marshal(limits); !errors.Is(err, trajectory.ErrInvalidSample) {
			t.Fatalf("negative limit serialized: %v", err)
		}
	}
}
