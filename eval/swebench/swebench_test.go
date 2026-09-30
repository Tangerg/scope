package swebench_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/swebench"
)

const (
	datasetRevision = "3d07b464b7b311a0cbfb5ed5b2d8a3b96f84a33d"
	patchText       = "diff --git a/code.py b/code.py\r\n--- a/code.py\r\n+++ b/code.py\r\n@@ -1 +1 @@\r\n-old\r\n+new\r\n"
)

func taskDigest(contract string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(contract)))
}

func selectionConfig() swebench.SelectionConfig {
	return swebench.SelectionConfig{
		Dataset: "SWE-bench/SWE-bench_Verified", Revision: datasetRevision, Split: "test",
		Tasks: []swebench.Task{
			{InstanceID: "case-resolved", Digest: taskDigest("resolved task and environment")},
			{InstanceID: "case-empty", Digest: taskDigest("empty task and environment")},
			{InstanceID: "case-error", Digest: taskDigest("error task and environment")},
			{InstanceID: "case-infra", Digest: taskDigest("infra task and environment")},
			{InstanceID: "case-invalid", Digest: taskDigest("invalid task and environment")},
			{InstanceID: "case-missing", Digest: taskDigest("missing task and environment")},
			{InstanceID: "case-unresolved", Digest: taskDigest("unresolved task and environment")},
		},
	}
}

func newSubmission(t *testing.T) swebench.Submission {
	t.Helper()
	selection, err := swebench.NewSelection(selectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	var predictions []swebench.Prediction
	for _, task := range selection.Tasks() {
		if task.InstanceID == "case-missing" {
			continue
		}
		patch := patchText
		if task.InstanceID == "case-empty" {
			patch = ""
		}
		predictions = append(predictions, swebench.Prediction{InstanceID: task.InstanceID, Patch: patch})
	}
	submission, err := swebench.NewSubmission(swebench.SubmissionConfig{
		Selection: selection, Model: "vendor/agent", AttemptID: "trial-1", Predictions: predictions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return submission
}

func TestSelectionIdentityAndOwnership(t *testing.T) {
	config := selectionConfig()
	selection, err := swebench.NewSelection(config)
	if err != nil {
		t.Fatal(err)
	}
	wantTasks := selection.Tasks()
	wantDigest := selection.Digest()
	if selection.Dataset() != config.Dataset || selection.Revision() != datasetRevision || selection.Split() != "test" || selection.Len() != 7 {
		t.Fatalf("selection identity lost: %#v", selection)
	}
	slices.Reverse(config.Tasks)
	reordered, err := swebench.NewSelection(config)
	if err != nil || reordered.Digest() != wantDigest {
		t.Fatalf("task ordering changed selection identity: %s, %v", reordered.Digest(), err)
	}
	config.Tasks[0].Digest = taskDigest("changed environment")
	changed, err := swebench.NewSelection(config)
	if err != nil || changed.Digest() == wantDigest {
		t.Fatalf("changed task contract did not change identity: %s, %v", changed.Digest(), err)
	}
	returned := selection.Tasks()
	returned[0].InstanceID = "mutated"
	if selection.Digest() != wantDigest || !reflect.DeepEqual(selection.Tasks(), wantTasks) {
		t.Fatal("caller mutation changed frozen selection")
	}
	for name, change := range map[string]func(*swebench.SelectionConfig){
		"dataset":  func(config *swebench.SelectionConfig) { config.Dataset += "_Lite" },
		"revision": func(config *swebench.SelectionConfig) { config.Revision = strings.Repeat("a", 40) },
		"split":    func(config *swebench.SelectionConfig) { config.Split = "dev" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := selectionConfig()
			change(&candidate)
			value, err := swebench.NewSelection(candidate)
			if err != nil || value.Digest() == wantDigest {
				t.Fatalf("changed %s did not change identity: %v", name, err)
			}
		})
	}
}

func TestSelectionRejectsAmbiguousIdentity(t *testing.T) {
	for name, change := range map[string]func(*swebench.SelectionConfig){
		"empty":          func(config *swebench.SelectionConfig) { config.Tasks = nil },
		"mutable ref":    func(config *swebench.SelectionConfig) { config.Revision = "main" },
		"missing source": func(config *swebench.SelectionConfig) { config.Dataset = "" },
		"bad digest":     func(config *swebench.SelectionConfig) { config.Tasks[0].Digest = "sha256:123" },
		"duplicate":      func(config *swebench.SelectionConfig) { config.Tasks = append(config.Tasks, config.Tasks[0]) },
		"traversal":      func(config *swebench.SelectionConfig) { config.Tasks[0].InstanceID = "../secret" },
		"dot":            func(config *swebench.SelectionConfig) { config.Tasks[0].InstanceID = "." },
	} {
		t.Run(name, func(t *testing.T) {
			config := selectionConfig()
			change(&config)
			if _, err := swebench.NewSelection(config); !errors.Is(err, swebench.ErrInvalidSelection) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSubmissionIdentityExportAndArguments(t *testing.T) {
	submission := newSubmission(t)
	config := swebench.SubmissionConfig{
		Selection: submission.Selection(), Model: submission.Model(), AttemptID: submission.AttemptID(), Predictions: submission.Predictions(),
	}
	slices.Reverse(config.Predictions)
	reordered, err := swebench.NewSubmission(config)
	if err != nil || reordered.RunID() != submission.RunID() {
		t.Fatalf("reordering changed run identity: %v", err)
	}
	for name, change := range map[string]func(*swebench.SubmissionConfig){
		"attempt":         func(config *swebench.SubmissionConfig) { config.AttemptID = "regrade-2" },
		"model directory": func(config *swebench.SubmissionConfig) { config.Model = "vendor__agent" },
		"patch bytes":     func(config *swebench.SubmissionConfig) { config.Predictions[0].Patch += "\n" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := config
			candidate.Predictions = slices.Clone(config.Predictions)
			change(&candidate)
			changed, changeErr := swebench.NewSubmission(candidate)
			if changeErr != nil || changed.RunID() == submission.RunID() {
				t.Fatalf("changed %s reused upstream cache identity: %v", name, changeErr)
			}
		})
	}
	var output bytes.Buffer
	if writeErr := submission.WritePredictions(&output); writeErr != nil {
		t.Fatal(writeErr)
	}
	wantFirst := "{\"instance_id\":\"case-empty\",\"model_name_or_path\":\"vendor/agent\",\"model_patch\":\"\"}\n"
	if !strings.HasPrefix(output.String(), wantFirst) || bytes.Count(output.Bytes(), []byte{'\n'}) != 6 {
		t.Fatalf("unexpected JSONL protocol: %s", output.String())
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), []byte{'\n'}) {
		var row predictionWire
		if decodeErr := jsonv2.Unmarshal(line, &row, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if row.Model != "vendor/agent" || (row.InstanceID != "case-empty" && row.Patch != patchText) {
			t.Fatalf("wire changed candidate bytes: %#v", row)
		}
	}
	arguments, err := submission.Arguments("data set.json", "predictions;literal.jsonl")
	wantArguments := []string{
		"-m", "swebench.harness.run_evaluation", "--dataset_name", "data set.json", "--split", "test",
		"--predictions_path", "predictions;literal.jsonl", "--run_id", submission.RunID(), "--instance_ids",
		"case-empty", "case-error", "case-infra", "case-invalid", "case-missing", "case-resolved", "case-unresolved",
	}
	if err != nil || !slices.Equal(arguments, wantArguments) {
		t.Fatalf("arguments = %q, error = %v", arguments, err)
	}
	config.Predictions[0].Patch = "mutated"
	returned := submission.Predictions()
	returned[0].Patch = "also mutated"
	var again bytes.Buffer
	if err := submission.WritePredictions(&again); err != nil || again.String() != output.String() {
		t.Fatalf("caller mutation changed submission: %v", err)
	}
}

func TestSubmissionRunIDEncodesExplicitIdentity(t *testing.T) {
	selection, err := swebench.NewSelection(swebench.SelectionConfig{
		Dataset: "SWE-bench/SWE-bench_Verified", Revision: datasetRevision, Split: "test",
		Tasks: []swebench.Task{{InstanceID: "case-a", Digest: taskDigest("a")}, {InstanceID: "case-b", Digest: taskDigest("b")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	submission, err := swebench.NewSubmission(swebench.SubmissionConfig{
		Selection: selection, Model: "vendor/agent", AttemptID: "trial-1",
		Predictions: []swebench.Prediction{{InstanceID: "case-b", Patch: "fix b"}, {InstanceID: "case-a", Patch: ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := `{"selection":"` + selection.Digest() + `","harness_revision":"` + swebench.HarnessRevision +
		`","model":"vendor/agent","attempt_id":"trial-1","predictions":[` +
		`{"instance_id":"case-a","model_patch":""},{"instance_id":"case-b","model_patch":"fix b"}]}`
	if want := fmt.Sprintf("%x", sha256.Sum256([]byte(identity))); submission.RunID() != want {
		t.Fatalf("RunID = %s, want %s", submission.RunID(), want)
	}
}

func TestSubmissionRejectsDuplicateAndForeignCandidates(t *testing.T) {
	submission := newSubmission(t)
	for name, change := range map[string]func(*swebench.SubmissionConfig){
		"duplicate": func(config *swebench.SubmissionConfig) {
			config.Predictions = append(config.Predictions, config.Predictions[0])
		},
		"outside selection": func(config *swebench.SubmissionConfig) { config.Predictions[0].InstanceID = "foreign" },
		"invalid UTF-8":     func(config *swebench.SubmissionConfig) { config.Predictions[0].Patch = string([]byte{0xff}) },
		"missing attempt":   func(config *swebench.SubmissionConfig) { config.AttemptID = "" },
		"unsafe model":      func(config *swebench.SubmissionConfig) { config.Model = ".." },
		"zero selection":    func(config *swebench.SubmissionConfig) { config.Selection = swebench.Selection{} },
	} {
		t.Run(name, func(t *testing.T) {
			config := swebench.SubmissionConfig{
				Selection: submission.Selection(), Model: submission.Model(), AttemptID: submission.AttemptID(), Predictions: submission.Predictions(),
			}
			change(&config)
			if _, err := swebench.NewSubmission(config); !errors.Is(err, swebench.ErrInvalidSubmission) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if err := submission.WritePredictions(failedWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error lost cause: %v", err)
	}
	var writer *bytes.Buffer
	if err := submission.WritePredictions(writer); !errors.Is(err, swebench.ErrInvalidSubmission) {
		t.Fatalf("typed nil writer: %v", err)
	}
}

type failedWriter struct{}

func (f failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type predictionWire struct {
	InstanceID string `json:"instance_id"`
	Model      string `json:"model_name_or_path"`
	Patch      string `json:"model_patch"`
}

// stageArtifacts deliberately uses exported JSONL as its only candidate input.
// Fixtures reproduce the pinned official wire shape; no Docker or model runs.
func stageArtifacts(t *testing.T, submission swebench.Submission) string {
	t.Helper()
	directory := t.TempDir()
	predictionPath := filepath.Join(directory, "predictions.jsonl")
	predictionFile, err := os.Create(predictionPath)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := submission.WritePredictions(predictionFile); writeErr != nil {
		predictionFile.Close()
		t.Fatal(writeErr)
	}
	if closeErr := predictionFile.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	input, err := os.Open(predictionPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		var row predictionWire
		if decodeErr := jsonv2.Unmarshal(scanner.Bytes(), &row, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if row.Patch == "" || row.InstanceID == "case-error" {
			continue
		}
		caseDirectory := filepath.Join(directory, "logs", "evaluation", submission.RunID(), strings.ReplaceAll(row.Model, "/", "__"), row.InstanceID)
		writeFile(t, filepath.Join(caseDirectory, "patch.diff"), []byte(row.Patch))
		var report []byte
		if row.InstanceID == "case-invalid" {
			report = []byte("{truncated")
		} else {
			report, err = os.ReadFile(filepath.Join("testdata", strings.TrimPrefix(row.InstanceID, "case-")+".json"))
			if err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, filepath.Join(caseDirectory, "report.json"), report)
	}
	if scanErr := scanner.Err(); scanErr != nil {
		t.Fatal(scanErr)
	}
	summary, err := os.ReadFile("testdata/results.json")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, "logs", "evaluation", submission.RunID(), "results.json"), summary)
	return directory
}

func writeFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func collectDirectory(t *testing.T, submission swebench.Submission, directory string) (swebench.Collection, error) {
	t.Helper()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return submission.Collect(root.FS())
}

func TestExportCollectImportPreservesOfficialOutcomes(t *testing.T) {
	submission := newSubmission(t)
	collection, err := collectDirectory(t, submission, stageArtifacts(t, submission))
	if err != nil {
		t.Fatal(err)
	}
	wantSummary := swebench.Summary{
		Total: 7, Submitted: 6, Completed: 4, Resolved: 1, Unresolved: 2,
		MissingPredictions: 1, EmptyPatches: 1, Errors: 2, InfrastructureFailures: 2, AmbiguousFailures: 1,
	}
	if collection.Summary() != wantSummary {
		t.Fatalf("summary = %#v, want %#v", collection.Summary(), wantSummary)
	}
	wantStatuses := []swebench.Status{
		swebench.StatusEmptyPatch, swebench.StatusError, swebench.StatusUnresolved, swebench.StatusError,
		swebench.StatusMissingPrediction, swebench.StatusResolved, swebench.StatusUnresolved,
	}
	results := collection.Cases()
	for index, result := range results {
		if result.Status() != wantStatuses[index] {
			t.Fatalf("%s status = %q, want %q", result.Task().InstanceID, result.Status(), wantStatuses[index])
		}
		report, err := result.Report()
		if result.Status() != swebench.StatusResolved && result.Status() != swebench.StatusUnresolved {
			if !errors.Is(err, swebench.ErrNoGrade) || report.Score != nil || result.GradeID() != "" {
				t.Fatalf("%s manufactured a grade: %#v, %v", result.Task().InstanceID, report, err)
			}
			continue
		}
		wantScore, wantVerdict := eval.Score(0), eval.VerdictFail
		if result.Status() == swebench.StatusResolved {
			wantScore, wantVerdict = 1, eval.VerdictPass
		}
		if err != nil || report.Score == nil || *report.Score != wantScore || report.Verdict() != wantVerdict || report.Decision.Policy != "swebench/resolved" {
			t.Fatalf("%s projection = %#v, %v", result.Task().InstanceID, report, err)
		}
		if report.Metric.String() != "swebench/resolved" || result.GradeID() == "" || result.CandidateDigest() == "" || result.ReportDigest() == "" {
			t.Fatalf("%s lost grade identity", result.Task().InstanceID)
		}
	}
	if !results[3].Completed() || results[3].Err() == nil || results[1].Completed() {
		t.Fatal("official completed count must distinguish corrupt and absent report files")
	}
	if results[1].Failure() != (swebench.Failure{Kind: swebench.FailureInfrastructure, Reason: "container_unavailable"}) ||
		results[2].Failure() != (swebench.Failure{Kind: swebench.FailureInfrastructure, Reason: "browser_launch_failed"}) ||
		results[3].Failure() != (swebench.Failure{Kind: swebench.FailureAmbiguous, Reason: "tests_timed_out"}) {
		t.Fatal("official additive triage was lost")
	}
	official, ok := results[5].Official()
	if !ok || !official.Resolved || !slices.Equal(official.TestsStatus.FailToPass.Success, []string{"test_fixed"}) {
		t.Fatalf("official evidence = %#v", official)
	}
	official.TestsStatus.FailToPass.Success[0] = "mutated"
	unchanged, _ := results[5].Official()
	if unchanged.TestsStatus.FailToPass.Success[0] != "test_fixed" {
		t.Fatal("official evidence was borrowed mutably")
	}
	results[0] = swebench.CaseResult{}
	if collection.Cases()[0].Status() != swebench.StatusEmptyPatch {
		t.Fatal("returned result container was borrowed mutably")
	}
}

func TestCollectRejectsCandidateAndReportMisattribution(t *testing.T) {
	for name, change := range map[string]func(*testing.T, string){
		"wrong candidate": func(t *testing.T, directory string) {
			writeFile(t, filepath.Join(directory, "patch.diff"), []byte(patchText+"\n"))
		},
		"unbound report": func(t *testing.T, directory string) {
			if err := os.Remove(filepath.Join(directory, "patch.diff")); err != nil {
				t.Fatal(err)
			}
		},
		"wrong instance": func(t *testing.T, directory string) {
			data, err := os.ReadFile(filepath.Join(directory, "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(directory, "report.json"), bytes.ReplaceAll(data, []byte("case-resolved"), []byte("other-instance")))
		},
		"null patch report": func(t *testing.T, directory string) {
			writeFile(t, filepath.Join(directory, "report.json"), []byte(`{"case-resolved":{"patch_is_None":true,"patch_exists":false,"patch_successfully_applied":false,"resolved":false,"infra_failure":false}}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			submission := newSubmission(t)
			directory := stageArtifacts(t, submission)
			change(t, filepath.Join(directory, "logs", "evaluation", submission.RunID(), "vendor__agent", "case-resolved"))
			collection, err := collectDirectory(t, submission, directory)
			if !errors.Is(err, swebench.ErrArtifactMismatch) || len(collection.Cases()) != 0 {
				t.Fatalf("unsafe collection returned: %#v, %v", collection, err)
			}
		})
	}
}

func TestGradeIdentityBindsAttemptAndExactReport(t *testing.T) {
	submission := newSubmission(t)
	directory := stageArtifacts(t, submission)
	baseline, err := collectDirectory(t, submission, directory)
	if err != nil {
		t.Fatal(err)
	}
	first := baseline.Cases()[5]
	reportPath := filepath.Join(directory, "logs", "evaluation", submission.RunID(), "vendor__agent", "case-resolved", "report.json")
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, reportPath, append(data, '\n'))
	changedReport, err := collectDirectory(t, submission, directory)
	if err != nil {
		t.Fatal(err)
	}
	second := changedReport.Cases()[5]
	if first.GradeID() == second.GradeID() || first.ReportDigest() == second.ReportDigest() || first.CandidateDigest() != second.CandidateDigest() {
		t.Fatal("grade identity did not bind exact report bytes independently of its candidate")
	}
	regrade, err := swebench.NewSubmission(swebench.SubmissionConfig{
		Selection: submission.Selection(), Model: submission.Model(), AttemptID: "regrade-2", Predictions: submission.Predictions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	newAttempt, err := collectDirectory(t, regrade, stageArtifacts(t, regrade))
	if err != nil {
		t.Fatal(err)
	}
	third := newAttempt.Cases()[5]
	if first.GradeID() == third.GradeID() || first.ReportDigest() != third.ReportDigest() || first.CandidateDigest() != third.CandidateDigest() {
		t.Fatal("regrading reused a grade identity or changed frozen candidate evidence")
	}
}

func TestCollectRejectsPartialOrInconsistentOfficialSummary(t *testing.T) {
	for name, change := range map[string]func([]byte) []byte{
		"single case denominator": func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"total_instances": 7`), []byte(`"total_instances": 1`), 1)
		},
		"extra submitted": func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"submitted_instances": 6`), []byte(`"submitted_instances": 7`), 1)
		},
		"duplicate IDs": func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"resolved_ids": ["case-resolved"]`), []byte(`"resolved_ids": ["case-resolved", "case-resolved"]`), 1)
		},
		"wrong diagnostics": func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"infra_failure_ids": ["case-error", "case-infra"]`), []byte(`"infra_failure_ids": ["case-resolved", "case-infra"]`), 1)
		},
		"unknown schema": func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"schema_version": 2`), []byte(`"schema_version": 3`), 1)
		},
		"unknown member": func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"schema_version": 2`), []byte(`"schema_version": 2, "unexpected": true`), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			submission := newSubmission(t)
			directory := stageArtifacts(t, submission)
			summaryPath := filepath.Join(directory, "logs", "evaluation", submission.RunID(), "results.json")
			data, err := os.ReadFile(summaryPath)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, summaryPath, change(data))
			if _, err := collectDirectory(t, submission, directory); !errors.Is(err, swebench.ErrInvalidArtifacts) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	submission := newSubmission(t)
	directory := stageArtifacts(t, submission)
	if err := os.Remove(filepath.Join(directory, "logs", "evaluation", submission.RunID(), "results.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := collectDirectory(t, submission, directory); !errors.Is(err, swebench.ErrInvalidArtifacts) {
		t.Fatalf("missing summary error = %v", err)
	}
}

func TestCollectRequiresStructurallyValidOfficialGrade(t *testing.T) {
	for name, replacement := range map[string]string{
		"missing bool":    `"resolved": true`,
		"null bool":       `"resolved": null`,
		"unknown field":   `"resolved": true, "retired": true`,
		"duplicate field": `"resolved": true, "resolved": false`,
		"contradictory":   `"resolved": true, "infra_failure_reason": "browser_launch_failed"`,
	} {
		t.Run(name, func(t *testing.T) {
			submission := newSubmission(t)
			directory := stageArtifacts(t, submission)
			reportPath := filepath.Join(directory, "logs", "evaluation", submission.RunID(), "vendor__agent", "case-resolved", "report.json")
			data, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing bool":
				data = bytes.Replace(data, []byte(`"resolved": true,`), nil, 1)
			case "contradictory":
				data = bytes.Replace(data, []byte(`"infra_failure": false`), []byte(`"infra_failure": true`), 1)
				data = bytes.Replace(data, []byte(`"resolved": true`), []byte(replacement), 1)
			default:
				data = bytes.Replace(data, []byte(`"resolved": true`), []byte(replacement), 1)
			}
			writeFile(t, reportPath, data)
			if _, err := collectDirectory(t, submission, directory); !errors.Is(err, swebench.ErrInvalidArtifacts) {
				t.Fatalf("invalid grade was accepted: %v", err)
			}
		})
	}
}
