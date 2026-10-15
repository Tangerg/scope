package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

const (
	minimumQualityScore     eval.Score = 1
	maximumRetainedFraction float64    = 0.5
)

func qualitySuite(t *testing.T) *eval.Suite[eval.TrialSample[contextTask, trajectory.Trajectory]] {
	t.Helper()
	qualityMetric, err := eval.NewMetric(eval.MetricConfig{Namespace: "context_example", Name: "answer_exact"})
	if err != nil {
		t.Fatal(err)
	}
	byteMetric, err := eval.NewMetric(eval.MetricConfig{Namespace: "context_example", Name: "model_request_bytes", Unit: "byte", Direction: eval.DirectionLowerIsBetter})
	if err != nil {
		t.Fatal(err)
	}
	type sample = eval.TrialSample[contextTask, trajectory.Trajectory]
	suite, err := eval.NewSuite(eval.SuiteConfig[sample]{Assessments: []eval.Assessment[sample]{
		{ID: "quality", Evaluator: eval.EvaluatorFunc[sample](func(_ context.Context, value sample) (eval.Report, error) {
			text, answerErr := answer(*value.Execution.Output)
			if answerErr != nil {
				return eval.Report{}, answerErr
			}
			score := eval.Score(0)
			if text == value.Case.Subject.Expected {
				score = 1
			}
			decision, decisionErr := score.Decide(minimumQualityScore)
			return eval.Report{Metric: qualityMetric, Score: &score, Decision: &decision}, decisionErr
		})},
		{ID: "execution", Evaluator: eval.EvaluatorFunc[sample](func(ctx context.Context, value sample) (eval.Report, error) {
			calls := make([]trajectory.ToolExpectation, len(value.Case.Subject.Documents))
			for index := range calls {
				arguments, encodeErr := jsonv2.Marshal(readReport{Index: index})
				if encodeErr != nil {
					return eval.Report{}, encodeErr
				}
				calls[index] = trajectory.ToolExpectation{Name: "read_report", Arguments: new(trajectory.ToolArguments(arguments)), Outcome: trajectory.ToolOutcomeSucceeded}
			}
			return (trajectory.Evaluator{}).Evaluate(ctx, trajectory.Sample{Actual: *value.Execution.Output, Expected: trajectory.Expectation{
				Status: agent.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: calls}, Limits: trajectory.Limits{DroppedDeltas: new(uint64(0))},
			}})
		})},
		{ID: "context", Evaluator: eval.EvaluatorFunc[sample](func(_ context.Context, value sample) (eval.Report, error) {
			bytes, measureErr := inputBytes(*value.Execution.Output)
			return eval.Report{Metric: byteMetric, Measurement: new(float64(bytes))}, measureErr
		})},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return suite
}

func TestContextReductionPairedQualityAndSize(t *testing.T) {
	ctx := t.Context()
	tasks := contextTasks()
	cases := make([]eval.Case[contextTask], len(tasks))
	for index, task := range tasks {
		cases[index] = eval.Case[contextTask]{ID: eval.CaseID(task.ID), Subject: task}
	}
	fixture, err := jsonv2.Marshal(cases, jsonv2.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := eval.NewDataset(agent.ComputeDigest(fixture).String(), cases...)
	if err != nil {
		t.Fatal(err)
	}
	suite := qualitySuite(t)
	var reports []eval.ExperimentReport
	var recordings [][]trajectory.Trajectory
	for _, policy := range []contextPolicy{{ID: unchangedContextPolicy}, {ID: duplicateOutputPolicyID, Reducer: duplicateToolOutputReducer{}}} {
		trial, trialErr := eval.NewTrial(eval.TrialConfig[contextTask, trajectory.Trajectory]{Suite: suite,
			Target: eval.TargetFunc[contextTask, trajectory.Trajectory](func(ctx context.Context, task contextTask) (eval.Execution[trajectory.Trajectory], error) {
				runContext, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				// Policy identity belongs to deployment configuration, not fixture
				// identity: both runs still grade the same inputs and references.
				recorded, runErr := runTask(runContext, task, policy)
				if runErr != nil {
					return eval.Execution[trajectory.Trajectory]{}, runErr
				}
				return eval.Execution[trajectory.Trajectory]{Status: eval.ExecutionCompleted, Output: &recorded}, nil
			}),
		})
		if trialErr != nil {
			t.Fatal(trialErr)
		}
		var results, regraded []eval.CaseResult
		var modeRecordings []trajectory.Trajectory
		for _, caseValue := range dataset.Cases() {
			result, runErr := trial.Run(ctx, caseValue)
			if runErr != nil || result.Execution == nil || !result.Case.Result.Complete() || result.Case.Result.Verdict() != eval.VerdictPass {
				t.Fatalf("case %s failed: result=%+v, error=%v", caseValue.ID, result, runErr)
			}
			encoded, encodeErr := jsonv2.Marshal(eval.TrialSample[contextTask, trajectory.Trajectory]{Case: caseValue, Execution: *result.Execution})
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			var retained eval.TrialSample[contextTask, trajectory.Trajectory]
			if decodeErr := jsonv2.Unmarshal(encoded, &retained, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			reassessment, gradeErr := suite.Run(ctx, retained)
			if gradeErr != nil {
				t.Fatal(gradeErr)
			}
			results = append(results, result.Case)
			regraded = append(regraded, eval.CaseResult{ID: retained.Case.ID, Result: reassessment})
			modeRecordings = append(modeRecordings, *retained.Execution.Output)
		}
		report, reportErr := eval.NewExperimentReport(dataset.FixtureID(), results)
		if reportErr != nil {
			t.Fatal(reportErr)
		}
		rescored, reportErr := eval.NewExperimentReport(dataset.FixtureID(), regraded)
		if reportErr != nil {
			t.Fatal(reportErr)
		}
		comparison, compareErr := report.Compare(rescored)
		if compareErr != nil {
			t.Fatal(compareErr)
		}
		for _, metric := range comparison.Metrics {
			if metric.ScoreDelta.Mean != 0 || metric.MeasurementDelta.Mean != 0 || metric.EvaluatedDelta != 0 {
				t.Fatalf("retained evidence changed its grade: %+v", metric)
			}
		}
		reports, recordings = append(reports, report), append(recordings, modeRecordings)
	}
	comparison, err := reports[0].Compare(reports[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range comparison.Metrics {
		if metric.AssessmentID == "quality" && (metric.ScoreDelta != (eval.DistributionDelta{Present: true, Matched: len(tasks)}) || metric.DecisionDelta != (eval.DecisionDelta{Matched: len(tasks)})) {
			t.Fatalf("quality exceeded the zero-degradation boundary: %+v", metric)
		}
	}
	for index, task := range tasks {
		baseline, candidate := recordings[0][index], recordings[1][index]
		baselineBytes, measureErr := inputBytes(baseline)
		if measureErr != nil {
			t.Fatal(measureErr)
		}
		candidateBytes, measureErr := inputBytes(candidate)
		if measureErr != nil {
			t.Fatal(measureErr)
		}
		if float64(candidateBytes) > float64(baselineBytes)*maximumRetainedFraction {
			t.Fatalf("%s: insufficient reduction: %d -> %d", task.ID, baselineBytes, candidateBytes)
		}
		baselineCalls, candidateCalls := baseline.ModelCalls(), candidate.ModelCalls()
		if len(baselineCalls) != reportRounds+1 || len(candidateCalls) != reportRounds+1 {
			t.Fatal("reduction changed model work")
		}
		for position, call := range candidateCalls {
			original := baselineCalls[position].Request
			if !reflect.DeepEqual(call.Request.Tools, original.Tools) || !reflect.DeepEqual(call.Request.ToolChoice, original.ToolChoice) || !reflect.DeepEqual(call.Request.Options, original.Options) || !reflect.DeepEqual(call.Request.Messages[:2], original.Messages[:2]) {
				t.Fatal("reduction changed authority, options, system constraints, or the user goal")
			}
		}
		for position, call := range candidate.ToolCalls() {
			original := baseline.ToolCalls()[position]
			if !reflect.DeepEqual(call.Call, original.Call) || !reflect.DeepEqual(call.Result, original.Result) {
				t.Fatal("context projection rewrote actual Tool receipts")
			}
		}
		t.Logf("%s: quality delta=0, model calls=%d, request bytes=%d -> %d (%.1f%% retained)", task.ID, len(candidate.ModelCalls()), baselineBytes, candidateBytes, 100*float64(candidateBytes)/float64(baselineBytes))
	}
}

// A deliberate evidence corruption must fail quality while the same model and
// Tool protocol still complete; otherwise the gate would only test execution.
type corruptingReducer struct{}

func (corruptingReducer) ReduceModelContext(_ context.Context, _ interaction.ModelInvocation, request *chat.Request) ([]chat.Message, error) {
	messages := request.Clone().Messages
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Kind == chat.PartToolResult {
				for index := range part.ToolResult.Output.Content {
					content := &part.ToolResult.Output.Content[index]
					content.Text = strings.ReplaceAll(content.Text, "ALPHA", "CORRUPTED")
				}
			}
		}
	}
	return messages, nil
}

func TestQualityGateDetectsCorruptedEvidence(t *testing.T) {
	task := contextTasks()[0]
	recorded, err := runTask(t.Context(), task, contextPolicy{ID: "corrupted_evidence_control", Reducer: corruptingReducer{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := qualitySuite(t).Run(t.Context(), eval.TrialSample[contextTask, trajectory.Trajectory]{
		Case:      eval.Case[contextTask]{ID: eval.CaseID(task.ID), Subject: task},
		Execution: eval.Execution[trajectory.Trajectory]{Status: eval.ExecutionCompleted, Output: &recorded},
	})
	if err != nil || !result.Complete() || result.Verdict() != eval.VerdictFail {
		t.Fatalf("evidence corruption escaped quality: %+v, %v", result, err)
	}
	for _, assessment := range result.Results {
		if assessment.ID == "quality" && (assessment.Report.Verdict() != eval.VerdictFail || assessment.Report.Score == nil || *assessment.Report.Score != 0) {
			t.Fatal("corrupted evidence did not fail the exact quality gate")
		}
		if assessment.ID == "execution" && assessment.Report.Verdict() != eval.VerdictPass {
			t.Fatal("negative control failed execution instead of quality")
		}
	}
}
