package swebench_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	jsonv2 "encoding/json/v2"
	"fmt"
	"path"
	"testing/fstest"

	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/swebench"
)

func ExampleSubmission_Collect() {
	check := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	ctx := context.Background()
	// A Host solver has finished collecting a frozen patch, independently of
	// grading. Real task identity covers the full task and environment contract.
	patch := "diff --git a/code.py b/code.py\n--- a/code.py\n+++ b/code.py\n@@ -1 +1 @@\n-old\n+new\n"
	execution := eval.Execution[string]{Status: eval.ExecutionCompleted, Output: &patch}
	check(execution.Validate())
	selection, err := swebench.NewSelection(swebench.SelectionConfig{
		Dataset:  "SWE-bench/SWE-bench_Verified",
		Revision: "3d07b464b7b311a0cbfb5ed5b2d8a3b96f84a33d", Split: "test",
		Tasks: []swebench.Task{{InstanceID: "case-resolved", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("fixture task and environment")))}},
	})
	check(err)
	submission, err := swebench.NewSubmission(swebench.SubmissionConfig{
		Selection: selection, Model: "host/solver", AttemptID: "trial-1",
		Predictions: []swebench.Prediction{{InstanceID: "case-resolved", Patch: *execution.Output}},
	})
	check(err)
	var predictions bytes.Buffer
	check(submission.WritePredictions(&predictions))
	arguments, err := submission.Arguments("frozen-dataset.json", "predictions.jsonl")
	check(err)
	fmt.Println("harness:", arguments[1])

	// A production Host writes predictions, runs HarnessRevision using the
	// arguments, and collects an immutable artifact snapshot. This checked
	// example uses official-protocol fixtures; it executes no Docker or model.
	var row struct {
		InstanceID string `json:"instance_id"`
		Model      string `json:"model_name_or_path"`
		Patch      string `json:"model_patch"`
	}
	check(jsonv2.Unmarshal(predictions.Bytes(), &row))
	runDirectory := path.Join("logs", "evaluation", submission.RunID())
	caseDirectory := path.Join(runDirectory, "host__solver", row.InstanceID)
	artifacts := fstest.MapFS{
		path.Join(caseDirectory, "patch.diff"): {Data: []byte(row.Patch)},
		path.Join(caseDirectory, "report.json"): {Data: []byte(`{
		  "case-resolved": {
		    "patch_is_None": false, "patch_exists": true,
		    "patch_successfully_applied": true, "resolved": true, "infra_failure": false,
		    "tests_status": {
		      "FAIL_TO_PASS": {"success": ["test_fixed"], "failure": []},
		      "PASS_TO_PASS": {"success": ["test_existing"], "failure": []},
		      "FAIL_TO_FAIL": {"success": [], "failure": []},
		      "PASS_TO_FAIL": {"success": [], "failure": []}
		    }
		  }
		}`)},
		path.Join(runDirectory, "results.json"): {Data: []byte(`{
		  "total_instances": 1, "submitted_instances": 1, "completed_instances": 1,
		  "resolved_instances": 1, "unresolved_instances": 0,
		  "infra_failure_instances": 0, "ambiguous_failure_instances": 0,
		  "empty_patch_instances": 0, "error_instances": 0,
		  "completed_ids": ["case-resolved"], "incomplete_ids": [], "empty_patch_ids": [],
		  "submitted_ids": ["case-resolved"], "resolved_ids": ["case-resolved"],
		  "unresolved_ids": [], "infra_failure_ids": [], "ambiguous_failure_ids": [],
		  "failure_reasons": {}, "error_ids": [], "schema_version": 2
		}`)},
	}
	collection, err := submission.Collect(artifacts)
	check(err)

	// Assessment identity remains declared even when a case has no grade.
	// Reassessment of collected evidence does not invoke the target again.
	suite, err := eval.NewSuite(eval.SuiteConfig[swebench.CaseResult]{
		Assessments: []eval.Assessment[swebench.CaseResult]{{
			ID: "official-resolution",
			Evaluator: eval.EvaluatorFunc[swebench.CaseResult](func(ctx context.Context, result swebench.CaseResult) (eval.Report, error) {
				if contextErr := ctx.Err(); contextErr != nil {
					return eval.Report{}, contextErr
				}
				return result.Report()
			}),
		}},
	})
	check(err)
	var cases []eval.Case[swebench.CaseResult]
	for _, result := range collection.Cases() {
		cases = append(cases, eval.Case[swebench.CaseResult]{ID: eval.CaseID(result.Task().InstanceID), Subject: result})
	}
	dataset, err := eval.NewDataset(selection.Digest(), cases...)
	check(err)
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[swebench.CaseResult]{Dataset: dataset, Suite: suite})
	check(err)
	report, err := experiment.Run(ctx)
	check(err)
	fmt.Printf("official: %d/%d resolved\n", collection.Summary().Resolved, collection.Summary().Total)
	fmt.Println("assessment:", report.Cases()[0].Result.Results[0].Status)
	fmt.Println("decision:", report.Cases()[0].Result.Results[0].Report.Verdict())
	// Output:
	// harness: swebench.harness.run_evaluation
	// official: 1/1 resolved
	// assessment: completed
	// decision: pass
}
