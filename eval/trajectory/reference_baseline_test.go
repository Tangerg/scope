package trajectory_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

const referenceMaximumTokens int64 = 20

type referenceFixture struct {
	Case        eval.Case[referenceInput]
	Status      agent.Status
	ToolOutcome trajectory.ToolOutcome
	Answer      string
	Writes      int
	Unknown     int
	Consumed    int
	Quality     eval.Verdict
	Tokens      *int64
}

func referenceFixtures() []referenceFixture {
	return []referenceFixture{
		{
			Case:   eval.Case[referenceInput]{ID: "approved", Subject: referenceInput{Note: "release note", Approved: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeSucceeded, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictPass, Tokens: new(int64(20)),
		},
		{
			Case:   eval.Case[referenceInput]{ID: "rejected", Subject: referenceInput{Note: "release note"}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeError, Answer: "rejected", Consumed: 1, Quality: eval.VerdictPass, Tokens: new(int64(20)),
		},
		{
			Case:   eval.Case[referenceInput]{ID: "repeated-input", Subject: referenceInput{Note: "release note", Approved: true, RepeatInput: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeSucceeded, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictPass, Tokens: new(int64(20)),
		},
		{
			Case:   eval.Case[referenceInput]{ID: "reconciled-write", Subject: referenceInput{Note: "release note", Approved: true, LoseResponse: true, Reconcile: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeUnknown, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictPass, Tokens: new(int64(20)),
		},
		{
			Case:   eval.Case[referenceInput]{ID: "unresolved-write", Subject: referenceInput{Note: "release note", Approved: true, LoseResponse: true}},
			Status: agent.StatusKilled, ToolOutcome: trajectory.ToolOutcomeUnknown, Answer: "stored:release note", Writes: 1, Unknown: 1, Quality: eval.VerdictFail, Tokens: new(int64(10)),
		},
		{
			Case:   eval.Case[referenceInput]{ID: "incorrect-answer", Subject: referenceInput{Note: "release note", Approved: true, WrongAnswer: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeSucceeded, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictFail, Tokens: new(int64(20)),
		},
		{
			Case:   eval.Case[referenceInput]{ID: "over-budget", Subject: referenceInput{Note: "release note", Approved: true, Expensive: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeSucceeded, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictPass, Tokens: new(int64(60)),
		},
		{
			Case:   eval.Case[referenceInput]{ID: "missing-usage", Subject: referenceInput{Note: "release note", Approved: true, MissingUsage: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeSucceeded, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictPass,
		},
	}
}

func referenceDataset(fixtures []referenceFixture) (eval.Dataset[referenceInput], error) {
	encoded, err := jsonv2.Marshal(fixtures, jsonv2.Deterministic(true))
	if err != nil {
		return eval.Dataset[referenceInput]{}, err
	}
	cases := make([]eval.Case[referenceInput], len(fixtures))
	for index, fixture := range fixtures {
		cases[index] = fixture.Case
	}
	return eval.NewDataset(agent.ComputeDigest(encoded).String(), cases...)
}

func referenceDecision(name eval.MetricName, passed bool, expected any) (eval.Report, error) {
	metric, err := eval.NewMetric(eval.MetricConfig{Namespace: "reference", Name: name})
	if err != nil {
		return eval.Report{}, err
	}
	parameters := metadata.Map{}
	if err := parameters.Set("expected", expected); err != nil {
		return eval.Report{}, err
	}
	verdict, score := eval.VerdictFail, eval.Score(0)
	if passed {
		verdict, score = eval.VerdictPass, 1
	}
	return eval.Report{Metric: metric, Decision: &eval.Decision{Policy: "reference.exact", Parameters: parameters, Verdict: verdict}, Score: &score}, nil
}

func referenceSuite(fixtures []referenceFixture, maximumTokens int64) (*eval.Suite[eval.TrialSample[referenceInput, referenceEvidence]], error) {
	byID := make(map[eval.CaseID]referenceFixture, len(fixtures))
	for _, fixture := range fixtures {
		byID[fixture.Case.ID] = fixture
	}
	assess := func(id eval.AssessmentID, evaluate func(context.Context, referenceEvidence, referenceFixture) (eval.Report, error)) eval.Assessment[eval.TrialSample[referenceInput, referenceEvidence]] {
		return lo.Must(eval.NewAssessment(eval.AssessmentConfig[eval.TrialSample[referenceInput, referenceEvidence]]{ID: id,
			Evaluator: eval.EvaluatorFunc[eval.TrialSample[referenceInput, referenceEvidence]](func(ctx context.Context, sample eval.TrialSample[referenceInput, referenceEvidence]) (eval.Report, error) {
				if err := ctx.Err(); err != nil {
					return eval.Report{}, err
				}
				fixture, exists := byID[sample.Case.ID]
				if !exists || fixture.Case.Subject != sample.Case.Subject {
					return eval.Report{}, errors.New("reference: case does not belong to this fixture")
				}
				if sample.Execution.Output == nil {
					return eval.Report{}, errors.New("reference: execution evidence is missing")
				}
				return evaluate(ctx, *sample.Execution.Output, fixture)
			}),
		}))
	}
	return eval.NewSuite(eval.SuiteConfig[eval.TrialSample[referenceInput, referenceEvidence]]{
		Assessments: []eval.Assessment[eval.TrialSample[referenceInput, referenceEvidence]]{
			assess("execution", func(ctx context.Context, evidence referenceEvidence, fixture referenceFixture) (eval.Report, error) {
				arguments, err := jsonv2.Marshal(referenceNote{Text: fixture.Case.Subject.Note})
				if err != nil {
					return eval.Report{}, err
				}
				return (trajectory.Evaluator{}).Evaluate(ctx, trajectory.Sample{
					Actual: evidence.Trajectory, Expected: trajectory.Expectation{
						Status: agent.StatusCompleted, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{
							{Name: "record_note", Arguments: new(trajectory.ToolArguments(arguments)), Outcome: trajectory.ToolOutcomeInputRequired},
							{Name: "record_note", Arguments: new(trajectory.ToolArguments(arguments)), Outcome: fixture.ToolOutcome},
						}},
						// Tree usage includes framework child controls and settlement
						// Signals; the business assessment counts external input alone.
						Limits: trajectory.Limits{PreparedEffects: new(uint64(7)), AcceptedSignals: new(uint64(9)), DroppedDeltas: new(uint64(0))},
					},
				})
			}),
			assess("quality", func(_ context.Context, evidence referenceEvidence, fixture referenceFixture) (eval.Report, error) {
				payload, completed := evidence.Trajectory.Termination().Output()
				if !completed {
					return referenceDecision("answer", false, fixture.Answer)
				}
				output, err := payload.Decode[interaction.Output]()
				if err != nil {
					return eval.Report{}, err
				}
				if err := output.Validate(); err != nil {
					return eval.Report{}, err
				}
				if output.ModelResponse == nil {
					return referenceDecision("answer", false, fixture.Answer)
				}
				text := output.ModelResponse.Output.Message.Text()
				return referenceDecision("answer", text == fixture.Answer, fixture.Answer)
			}),
			assess("business", func(_ context.Context, evidence referenceEvidence, fixture referenceFixture) (eval.Report, error) {
				unknown, receipts, consumed := 0, 0, 0
				for _, process := range evidence.Head.ProcessSnapshots() {
					unknown += len(process.UnknownEffectIDs())
					for _, receipt := range process.SignalReceipts() {
						if receipt.Matches(evidence.Input) {
							receipts++
							if receipt.Consumed() {
								consumed++
							}
						}
					}
				}
				passed := len(evidence.Rows) == fixture.Writes && evidence.WritesBeforeInput == 0 && unknown == fixture.Unknown && receipts == 1 && consumed == fixture.Consumed && evidence.Head.RootID() == evidence.Trajectory.RootProcessID()
				for _, row := range evidence.Rows {
					matched := false
					for _, call := range evidence.Trajectory.ToolCalls() {
						if call.EffectID == row.EffectID && call.Outcome() != trajectory.ToolOutcomeInputRequired {
							for _, process := range evidence.Head.ProcessSnapshots() {
								if process.Relation().ProcessID() == call.ProcessID {
									matched = call.TreeIncarnationID == evidence.Head.IncarnationID() && row.Note.Text == fixture.Case.Subject.Note
								}
							}
						}
					}
					passed = passed && matched
				}
				report, err := referenceDecision("business", passed, struct {
					Writes   int `json:"writes"`
					Unknown  int `json:"unknown"`
					Consumed int `json:"consumed"`
				}{fixture.Writes, fixture.Unknown, fixture.Consumed})
				report.Feedback = fmt.Sprintf("writes=%d, writes_before_input=%d, unknown=%d, input_receipts=%d, consumed=%d", len(evidence.Rows), evidence.WritesBeforeInput, unknown, receipts, consumed)
				return report, err
			}),
			assess("cost", func(ctx context.Context, evidence referenceEvidence, fixture referenceFixture) (eval.Report, error) {
				report, err := (trajectory.Evaluator{}).Evaluate(ctx, trajectory.Sample{
					Actual: evidence.Trajectory, Expected: trajectory.Expectation{
						Status: fixture.Status, Limits: trajectory.Limits{TotalTokens: &maximumTokens},
					},
				})
				if err != nil {
					return eval.Report{}, err
				}
				// Keep the canonical token measurement as the top-level observation;
				// the execution outcome has its own assessment and summary.
				for _, detail := range report.Details {
					if detail.Metric.Name() == trajectory.MetricTotalTokens {
						return detail, nil
					}
				}
				return eval.Report{}, errors.New("reference: token evaluation produced no measurement")
			}),
			assess("coverage", func(_ context.Context, evidence referenceEvidence, _ referenceFixture) (eval.Report, error) {
				coverageErr := evidence.Trajectory.CheckCoverage()
				if coverageErr != nil && !errors.Is(coverageErr, trajectory.ErrIncompleteRecording) {
					return eval.Report{}, coverageErr
				}
				report, err := referenceDecision("observation_coverage", coverageErr == nil, "complete history and semantic observations")
				if coverageErr != nil {
					report.Feedback = coverageErr.Error()
				}
				return report, err
			}),
		},
	})
}

func TestReferenceBaseline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	fixtures := referenceFixtures()
	dataset, err := referenceDataset(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := referenceSuite(fixtures, referenceMaximumTokens)
	if err != nil {
		t.Fatal(err)
	}
	invocations := 0
	trial, err := eval.NewTrial(eval.TrialConfig[referenceInput, referenceEvidence]{
		Target: eval.TargetFunc[referenceInput, referenceEvidence](func(ctx context.Context, input referenceInput) (eval.Execution[referenceEvidence], error) {
			invocations++
			return runReferenceTask(ctx, input)
		}), Suite: suite,
	})
	if err != nil {
		t.Fatal(err)
	}
	var original, regraded []eval.CaseResult
	var samples []eval.TrialSample[referenceInput, referenceEvidence]
	for index, caseValue := range dataset.Cases() {
		result, runErr := trial.Run(ctx, caseValue)
		if runErr != nil || result.Execution == nil {
			t.Fatalf("case %s: run=%v; execution=%v", caseValue.ID, runErr, result.Execution)
		}
		// Regrading consumes strict portable evidence, not a Process handle,
		// live workspace, second model generation, or reconstructed runtime.
		encoded, encodeErr := jsonv2.Marshal(eval.TrialSample[referenceInput, referenceEvidence]{Case: caseValue, Execution: *result.Execution})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		var retained eval.TrialSample[referenceInput, referenceEvidence]
		if decodeErr := jsonv2.Unmarshal(encoded, &retained, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		samples = append(samples, retained)
		reassessment, gradeErr := suite.Run(ctx, retained)
		if gradeErr != nil {
			t.Fatal(gradeErr)
		}
		original = append(original, result.Case)
		regraded = append(regraded, eval.CaseResult{ID: caseValue.ID, Result: reassessment})
		t.Run(caseValue.ID.String(), func(t *testing.T) {
			expectedStatus := eval.ExecutionCompleted
			if fixtures[index].Status == agent.StatusKilled {
				expectedStatus = eval.ExecutionCanceled
			}
			if result.Execution.Status != expectedStatus {
				t.Fatalf("execution status: %s", result.Execution.Status)
			}
			for _, assessment := range result.Case.Result.Results {
				if assessment.ID == "cost" && fixtures[index].Tokens == nil {
					if !errors.Is(assessment.Err, trajectory.ErrIncompleteRecording) || assessment.Report != nil {
						t.Fatalf("missing token accounting became a measurement: %+v", assessment)
					}
					continue
				}
				want := eval.VerdictPass
				if assessment.ID == "quality" {
					want = fixtures[index].Quality
				}
				if assessment.ID == "execution" && fixtures[index].Status != agent.StatusCompleted {
					want = eval.VerdictFail
				}
				if assessment.ID == "cost" && *fixtures[index].Tokens > referenceMaximumTokens {
					want = eval.VerdictFail
				}
				if assessment.Err != nil {
					t.Fatalf("assessment %s: %v", assessment.ID, assessment.Err)
				}
				if assessment.Report.Verdict() != want {
					encoded, reportErr := jsonv2.Marshal(assessment.Report)
					if reportErr != nil {
						t.Fatal(reportErr)
					}
					t.Fatalf("assessment %s: %s, want %s", assessment.ID, encoded, want)
				}
				if assessment.ID == "cost" && (assessment.Report.Measurement == nil || *assessment.Report.Measurement != float64(*fixtures[index].Tokens)) {
					t.Fatalf("token accounting changed: %+v", assessment.Report)
				}
			}
			complete, verdict := true, eval.VerdictPass
			if fixtures[index].Tokens == nil {
				complete, verdict = false, eval.VerdictUnspecified
			} else if fixtures[index].Quality == eval.VerdictFail || fixtures[index].Status != agent.StatusCompleted || *fixtures[index].Tokens > referenceMaximumTokens {
				verdict = eval.VerdictFail
			}
			if result.Case.Result.Complete() != complete || result.Case.Result.Verdict() != verdict {
				t.Fatalf("task gate: complete=%t, verdict=%s", result.Case.Result.Complete(), result.Case.Result.Verdict())
			}
		})
	}
	baseline, err := eval.NewExperimentReport(dataset.FixtureID(), original)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := eval.NewExperimentReport(dataset.FixtureID(), regraded)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := baseline.Compare(candidate)
	if err != nil {
		t.Fatal(err)
	}
	summary := baseline.Summary()
	if invocations != len(fixtures) || summary.Total != 8 || summary.Evaluated != 7 || summary.Passed != 4 || summary.Failed != 3 || summary.Errors != 1 || summary.Partial != 1 {
		t.Fatalf("discarded or regenerated samples: calls=%d, summary=%+v", invocations, baseline.Summary())
	}
	if len(comparison.Metrics) != 5 {
		t.Fatalf("missing assessment metrics: %+v", comparison.Metrics)
	}
	for _, metric := range comparison.Metrics {
		matched := len(fixtures)
		if metric.AssessmentID == "cost" {
			matched--
			if metric.MeasurementDelta != (eval.DistributionDelta{Present: true, Matched: matched}) || metric.Baseline.Measurements.Count != matched || metric.Baseline.Passed != 6 || metric.Baseline.Failed != 1 {
				t.Fatalf("cost evidence was lost: %+v", metric)
			}
		}
		if metric.EvaluatedDelta != 0 || metric.DecisionDelta != (eval.DecisionDelta{Matched: matched}) || metric.ScoreDelta.Mean != 0 {
			t.Fatalf("regrading changed comparable evidence: %+v", metric)
		}
		if metric.AssessmentID == "quality" && (metric.Baseline.Passed != 6 || metric.Baseline.Failed != 2) {
			t.Fatalf("quality failures were discarded: %+v", metric.Baseline)
		}
	}

	stricter, err := referenceSuite(fixtures, 10)
	if err != nil {
		t.Fatal(err)
	}
	var rescored []eval.CaseResult
	for index := len(samples) - 1; index >= 0; index-- {
		sample := samples[index]
		result, gradeErr := stricter.Run(ctx, sample)
		if gradeErr != nil {
			t.Fatal(gradeErr)
		}
		rescored = append(rescored, eval.CaseResult{ID: sample.Case.ID, Result: result})
	}
	strictReport, err := eval.NewExperimentReport(dataset.FixtureID(), rescored)
	if err != nil {
		t.Fatal(err)
	}
	review, err := baseline.Compare(strictReport)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range review.Metrics {
		if metric.AssessmentID == "cost" {
			if metric.MeasurementDelta != (eval.DistributionDelta{Present: true, Matched: 7}) || metric.DecisionDelta != (eval.DecisionDelta{Incompatible: 7}) || metric.Candidate.Passed != 1 || metric.Candidate.Failed != 6 {
				t.Fatalf("changed budget became a cost regression: %+v", metric)
			}
		} else if metric.DecisionDelta != (eval.DecisionDelta{Matched: 8}) {
			t.Fatalf("budget changed an independent assessment: %+v", metric)
		}
	}
	changedFixtures := referenceFixtures()
	changedFixtures[0].Case.Subject.Note = "another note"
	changedDataset, err := referenceDataset(changedFixtures)
	if err != nil {
		t.Fatal(err)
	}
	changedReport, err := eval.NewExperimentReport(changedDataset.FixtureID(), rescored)
	if err != nil {
		t.Fatal(err)
	}
	if _, compareErr := baseline.Compare(changedReport); !errors.Is(compareErr, eval.ErrInvalidComparison) {
		t.Fatalf("changed input accepted as the same fixture: %v", compareErr)
	}
	if invocations != len(fixtures) {
		t.Fatalf("regrading regenerated samples: %d calls", invocations)
	}
}

func TestReferenceMissingObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fixtures := referenceFixtures()
	execution, err := runReferenceTask(ctx, fixtures[0].Case.Subject)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := referenceSuite(fixtures, referenceMaximumTokens)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := suite.Run(ctx, eval.TrialSample[referenceInput, referenceEvidence]{Case: fixtures[0].Case, Execution: execution})
	if err != nil {
		t.Fatal(err)
	}
	recorded := execution.Output.Trajectory
	missing, err := trajectory.New(trajectory.Config{
		RootProcessID: recorded.RootProcessID(), Termination: recorded.Termination(), RootUsage: recorded.RootUsage(),
		Coverage: recorded.Coverage(), Gaps: recorded.Gaps(), Elapsed: recorded.Elapsed(), Events: recorded.Events(),
		ModelCalls: recorded.ModelCalls()[1:], ToolCalls: recorded.ToolCalls(),
	})
	if err != nil {
		t.Fatal(err)
	}
	execution.Output.Trajectory = missing
	result, err := suite.Run(ctx, eval.TrialSample[referenceInput, referenceEvidence]{Case: fixtures[0].Case, Execution: execution})
	if err != nil {
		t.Fatal(err)
	}
	for _, assessment := range result.Results {
		if assessment.ID == "execution" || assessment.ID == "cost" {
			if !errors.Is(assessment.Err, trajectory.ErrIncompleteRecording) || assessment.Report != nil {
				t.Fatalf("missing observation escaped coverage: %+v", assessment)
			}
		} else if assessment.ID == "coverage" {
			if assessment.Err != nil || assessment.Report.Verdict() != eval.VerdictFail {
				t.Fatalf("missing observation passed coverage: %+v", assessment)
			}
		} else if assessment.Err != nil || assessment.Report.Verdict() != eval.VerdictPass {
			t.Fatalf("independent assessment was lost: %+v", assessment)
		}
	}
	dataset, err := referenceDataset(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := eval.NewExperimentReport(dataset.FixtureID(), []eval.CaseResult{{ID: fixtures[0].Case.ID, Result: complete}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := eval.NewExperimentReport(dataset.FixtureID(), []eval.CaseResult{{ID: fixtures[0].Case.ID, Result: result}})
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := baseline.Compare(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.ErrorDelta != 1 || comparison.EvaluatedDelta != -1 || candidate.Summary().Partial != 1 {
		t.Fatalf("lost observations were discarded: %+v", comparison)
	}
	for _, metric := range comparison.Metrics {
		switch metric.AssessmentID {
		case "cost":
			if metric.Candidate != nil || metric.MeasurementDelta != (eval.DistributionDelta{BaselineOnly: 1}) {
				t.Fatalf("missing observations became cheaper execution: %+v", metric)
			}
		case "execution":
			if metric.Candidate != nil || metric.DecisionDelta != (eval.DecisionDelta{BaselineOnly: 1}) {
				t.Fatalf("missing observations became known execution: %+v", metric)
			}
		case "coverage":
			if metric.DecisionDelta != (eval.DecisionDelta{Matched: 1, PassedDelta: -1, FailedDelta: 1}) || metric.ScoreDelta != (eval.DistributionDelta{Present: true, Matched: 1, Mean: -1}) {
				t.Fatalf("observation loss was not comparable: %+v", metric)
			}
		default:
			if metric.DecisionDelta != (eval.DecisionDelta{Matched: 1}) || metric.ScoreDelta.Mean != 0 {
				t.Fatalf("independent evidence changed: %+v", metric)
			}
		}
	}
}

// Example_referenceBaseline retains inputs and execution receipts together.
// Reassessment invokes only Suite, so a new budget cannot change the recorded
// outcome, regenerate a failed sample, or manufacture missing accounting.
func Example_referenceBaseline() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixtures := referenceFixtures()
	dataset, err := referenceDataset(fixtures)
	if err != nil {
		panic(err)
	}
	suite, err := referenceSuite(fixtures, referenceMaximumTokens)
	if err != nil {
		panic(err)
	}
	trial, err := eval.NewTrial(eval.TrialConfig[referenceInput, referenceEvidence]{
		Target: eval.TargetFunc[referenceInput, referenceEvidence](runReferenceTask), Suite: suite,
	})
	if err != nil {
		panic(err)
	}
	var samples []eval.TrialSample[referenceInput, referenceEvidence]
	var original []eval.CaseResult
	for _, caseValue := range dataset.Cases() {
		result, runErr := trial.Run(ctx, caseValue)
		if runErr != nil {
			panic(runErr)
		}
		samples = append(samples, eval.TrialSample[referenceInput, referenceEvidence]{Case: caseValue, Execution: *result.Execution})
		original = append(original, result.Case)
	}
	encoded, err := jsonv2.Marshal(samples)
	if err != nil {
		panic(err)
	}
	var retained []eval.TrialSample[referenceInput, referenceEvidence]
	if decodeErr := jsonv2.Unmarshal(encoded, &retained, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
		panic(decodeErr)
	}
	stricter, err := referenceSuite(fixtures, 10)
	if err != nil {
		panic(err)
	}
	var regraded []eval.CaseResult
	for _, sample := range retained {
		result, gradeErr := stricter.Run(ctx, sample)
		if gradeErr != nil {
			panic(gradeErr)
		}
		regraded = append(regraded, eval.CaseResult{ID: sample.Case.ID, Result: result})
	}
	baseline, err := eval.NewExperimentReport(dataset.FixtureID(), original)
	if err != nil {
		panic(err)
	}
	candidate, err := eval.NewExperimentReport(dataset.FixtureID(), regraded)
	if err != nil {
		panic(err)
	}
	comparison, err := baseline.Compare(candidate)
	if err != nil {
		panic(err)
	}
	summary := baseline.Summary()
	fmt.Printf("retained: %d cases, %d failed, %d incomplete\n", summary.Total, summary.Failed, summary.Errors)
	for _, metric := range comparison.Metrics {
		if metric.AssessmentID == "cost" {
			fmt.Printf("tokens: %d paired, delta %.0f\n", metric.MeasurementDelta.Matched, metric.MeasurementDelta.Mean)
			fmt.Printf("budget: %d incompatible decisions\n", metric.DecisionDelta.Incompatible)
		}
	}
	// Output:
	// retained: 8 cases, 3 failed, 1 incomplete
	// tokens: 7 paired, delta 0
	// budget: 7 incompatible decisions
}
