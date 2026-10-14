package trajectory_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

type referenceFixture struct {
	Case        eval.Case[referenceInput]
	Status      agent.Status
	ToolOutcome trajectory.ToolOutcome
	Answer      string
	Writes      int
	Unknown     int
	Consumed    int
	Quality     eval.Verdict
}

func referenceFixtures() []referenceFixture {
	return []referenceFixture{
		{
			Case:   eval.Case[referenceInput]{ID: "approved", Subject: referenceInput{Note: "release note", Approved: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeSucceeded, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictPass,
		},
		{
			Case:   eval.Case[referenceInput]{ID: "rejected", Subject: referenceInput{Note: "release note"}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeError, Answer: "rejected", Consumed: 1, Quality: eval.VerdictPass,
		},
		{
			Case:   eval.Case[referenceInput]{ID: "repeated-input", Subject: referenceInput{Note: "release note", Approved: true, RepeatInput: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeSucceeded, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictPass,
		},
		{
			Case:   eval.Case[referenceInput]{ID: "reconciled-write", Subject: referenceInput{Note: "release note", Approved: true, LoseResponse: true, Reconcile: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeUnknown, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictPass,
		},
		{
			Case:   eval.Case[referenceInput]{ID: "unresolved-write", Subject: referenceInput{Note: "release note", Approved: true, LoseResponse: true}},
			Status: agent.StatusKilled, ToolOutcome: trajectory.ToolOutcomeUnknown, Answer: "stored:release note", Writes: 1, Unknown: 1, Quality: eval.VerdictFail,
		},
		{
			Case:   eval.Case[referenceInput]{ID: "incorrect-answer", Subject: referenceInput{Note: "release note", Approved: true, WrongAnswer: true}},
			Status: agent.StatusCompleted, ToolOutcome: trajectory.ToolOutcomeSucceeded, Answer: "stored:release note", Writes: 1, Consumed: 1, Quality: eval.VerdictFail,
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

func referenceSuite(fixtures []referenceFixture) (*eval.Suite[eval.TrialSample[referenceInput, referenceEvidence]], error) {
	byID := make(map[eval.CaseID]referenceFixture, len(fixtures))
	for _, fixture := range fixtures {
		byID[fixture.Case.ID] = fixture
	}
	assess := func(id eval.AssessmentID, evaluate func(context.Context, referenceEvidence, referenceFixture) (eval.Report, error)) eval.Assessment[eval.TrialSample[referenceInput, referenceEvidence]] {
		return eval.Assessment[eval.TrialSample[referenceInput, referenceEvidence]]{ID: id,
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
		}
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
						Status: fixture.Status, Tools: &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{
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
				return (trajectory.Evaluator{}).Evaluate(ctx, trajectory.Sample{
					Actual: evidence.Trajectory, Expected: trajectory.Expectation{
						Status: fixture.Status, Limits: trajectory.Limits{TotalTokens: new(int64(0))},
					},
				})
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
	suite, err := referenceSuite(fixtures)
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
	for index, caseValue := range dataset.Cases() {
		result, runErr := trial.Run(ctx, caseValue)
		if runErr != nil || result.Execution == nil {
			t.Fatalf("case %s: run=%v; execution=%v", caseValue.ID, runErr, result.Execution)
		}
		// Regrading consumes strict portable evidence, not a Process handle,
		// live workspace, second model generation, or reconstructed runtime.
		encoded, encodeErr := jsonv2.Marshal(result.Execution)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		var retained eval.Execution[referenceEvidence]
		if decodeErr := jsonv2.Unmarshal(encoded, &retained, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		reassessment, gradeErr := suite.Run(ctx, eval.TrialSample[referenceInput, referenceEvidence]{Case: caseValue, Execution: retained})
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
				if assessment.ID == "cost" {
					if !errors.Is(assessment.Err, trajectory.ErrIncompleteRecording) || assessment.Report != nil {
						t.Fatalf("missing token accounting became a measurement: %+v", assessment)
					}
					continue
				}
				want := eval.VerdictPass
				if assessment.ID == "quality" {
					want = fixtures[index].Quality
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
			}
			if result.Case.Result.Complete() || result.Case.Result.Verdict() != eval.VerdictUnspecified {
				t.Fatal("incomplete cost coverage passed the full gate")
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
	if invocations != len(fixtures) || baseline.Summary().Total != len(fixtures) || baseline.Summary().Partial != len(fixtures) {
		t.Fatalf("discarded or regenerated samples: calls=%d, summary=%+v", invocations, baseline.Summary())
	}
	for _, metric := range comparison.Metrics {
		if metric.EvaluatedDelta != 0 || metric.DecisionDelta.Matched != len(fixtures) || metric.DecisionDelta.Incompatible != 0 || metric.DecisionDelta.PassedDelta != 0 || metric.ScoreDelta.Mean != 0 {
			t.Fatalf("regrading changed comparable evidence: %+v", metric)
		}
		if metric.AssessmentID == "quality" && (metric.Baseline.Passed != 4 || metric.Baseline.Failed != 2) {
			t.Fatalf("quality failures were discarded: %+v", metric.Baseline)
		}
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
	suite, err := referenceSuite(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	result, err := suite.Run(ctx, eval.TrialSample[referenceInput, referenceEvidence]{Case: fixtures[0].Case, Execution: execution})
	if err != nil {
		t.Fatal(err)
	}
	for _, assessment := range result.Results {
		if assessment.ID == "execution" || assessment.ID == "cost" {
			if !errors.Is(assessment.Err, trajectory.ErrIncompleteRecording) || assessment.Report != nil {
				t.Fatalf("missing observation escaped coverage: %+v", assessment)
			}
		} else if assessment.Err != nil || assessment.Report.Verdict() != eval.VerdictPass {
			t.Fatalf("independent assessment was lost: %+v", assessment)
		}
	}
}
