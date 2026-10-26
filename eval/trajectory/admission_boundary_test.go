package trajectory_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestExpectationRejectsInvalidAssertionsBeforeEvaluation(t *testing.T) {
	invalidArguments := trajectory.ToolArguments(`{"value":`)
	for _, expected := range []trajectory.Expectation{
		{},
		{Status: agent.StatusFailed, Output: recordedOutput(t, runTrajectory(t))},
		{Status: agent.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: " "}}}},
		{Status: agent.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: "tool", Arguments: &invalidArguments}}}},
		{Status: agent.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{{Name: "tool", Outcome: "unsupported"}}}},
		{Status: agent.StatusCompleted, Baseline: new(trajectory.Trajectory)},
		{Status: agent.StatusCompleted, Limits: trajectory.Limits{TotalTokens: new(int64(-1))}},
		{Status: agent.StatusCompleted, Limits: trajectory.Limits{Elapsed: new(-time.Nanosecond)}},
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
		projection func(agent.Payload) (json.RawMessage, error)
		want       error
	}{
		{"projection failure", func(agent.Payload) (json.RawMessage, error) { return nil, projectionErr }, projectionErr},
		{"invalid JSON", func(agent.Payload) (json.RawMessage, error) { return json.RawMessage(`{"value":`), nil }, trajectory.ErrInvalidSample},
		{"duplicate member", func(agent.Payload) (json.RawMessage, error) { return json.RawMessage(`{"value":1,"value":2}`), nil }, trajectory.ErrInvalidSample},
	} {
		t.Run(test.name, func(t *testing.T) {
			report, err := (trajectory.Evaluator{OutputProjection: test.projection}).Evaluate(t.Context(), trajectory.Sample{
				Actual: actual, Expected: trajectory.Expectation{Status: agent.StatusCompleted, Baseline: &actual},
			})
			if !errors.Is(err, test.want) || report.Metric.Name() != "" {
				t.Fatalf("projection failure became a verdict: %+v, %v", report, err)
			}
		})
	}
}

func TestRecordedCallsRejectInvalidRequestsAndResults(t *testing.T) {
	recorded := coveredInteraction(t)
	for _, test := range []struct {
		name   string
		change func(*trajectory.ModelCall)
	}{
		{"missing request", func(call *trajectory.ModelCall) { call.Request = nil }},
		{"invalid request", func(call *trajectory.ModelCall) { call.Request = new(chat.Request) }},
		{"unobserved response", func(call *trajectory.ModelCall) { call.Outcome = interaction.ModelOutcomeUnobserved }},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := recorded.ModelCalls()[0]
			test.change(&call)
			if err := call.Validate(); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
				t.Fatalf("invalid model call admitted: %v", err)
			}
			config := trajectoryConfig(recorded)
			config.ModelCalls[0] = call
			if _, err := trajectory.New(config); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
				t.Fatalf("trajectory admitted invalid model call: %v", err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		change func(*trajectory.ToolCall)
	}{
		{"invalid call", func(call *trajectory.ToolCall) { call.Call.ID = "" }},
		{"invalid arguments", func(call *trajectory.ToolCall) { call.Call.Arguments = `{"x":` }},
		{"invalid result", func(call *trajectory.ToolCall) { call.Result.Name = "" }},
		{"wrong result owner", func(call *trajectory.ToolCall) { call.Result.ID = "another-call" }},
		{"invalid evidence", func(call *trajectory.ToolCall) {
			call.Result, call.Unknown = nil, true
			call.Evidence = new(chat.ToolOutput{Content: []chat.ToolContent{{}}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := recorded.ToolCalls()[0]
			test.change(&call)
			if err := call.Validate(); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
				t.Fatalf("invalid tool call admitted: %v", err)
			}
			config := trajectoryConfig(recorded)
			config.ToolCalls[0] = call
			if _, err := trajectory.New(config); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
				t.Fatalf("trajectory admitted invalid tool call: %v", err)
			}
		})
	}
}

func TestTrajectoryRejectsForeignModelOwnershipAndTokenOverflow(t *testing.T) {
	recorded := coveredInteraction(t)
	config := trajectoryConfig(recorded)
	foreign, err := agent.ParseProcessID("process:foreign")
	if err != nil {
		t.Fatal(err)
	}
	config.ModelCalls[0].ProcessID = foreign
	if _, configErr := trajectory.New(config); !errors.Is(configErr, trajectory.ErrInvalidTrajectory) {
		t.Fatalf("foreign model call became tree evidence: %v", configErr)
	}
	config = trajectoryConfig(recorded)
	config.ModelCalls[0].Response.Metadata.Usage = &chat.Usage{InputTokens: math.MaxInt64}
	overflow, err := trajectory.New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overflow.TotalTokens(); !errors.Is(err, trajectory.ErrInvalidTrajectory) {
		t.Fatalf("overflowed token total became a valid measurement: %v", err)
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
