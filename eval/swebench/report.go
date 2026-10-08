package swebench

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

// Status separates an official quality outcome from absent predictions and
// harness failures. Infrastructure advice never changes a resolved denominator
// or turns an official unresolved result into an absent grade.
type Status string

const (
	StatusMissingPrediction Status = "missing_prediction"
	StatusEmptyPatch        Status = "empty_patch"
	StatusError             Status = "error"
	StatusResolved          Status = "resolved"
	StatusUnresolved        Status = "unresolved"
)

func (s Status) classifiable() bool {
	return s == StatusError || s == StatusUnresolved
}

type FailureKind string

const (
	FailureUnspecified    FailureKind = ""
	FailureInfrastructure FailureKind = "infrastructure"
	FailureAmbiguous      FailureKind = "ambiguous"
)

// Failure preserves the official run summary's additive diagnostic advice.
// The harness can classify logs for an error even when no case report exists.
type Failure struct {
	Kind   FailureKind
	Reason string
}

// TestResults contains the official grader's test identities without
// recalculating its parser-specific resolution or maintenance rules.
type TestResults struct {
	Success []string `json:"success"`
	Failure []string `json:"failure"`
}

func (t TestResults) clone() TestResults {
	return TestResults{Success: slices.Clone(t.Success), Failure: slices.Clone(t.Failure)}
}

// TestsStatus retains the four official test-transition groups. Values returned
// by CaseResult.Official own their slices and may be edited by the caller.
type TestsStatus struct {
	FailToPass TestResults `json:"FAIL_TO_PASS"`
	PassToPass TestResults `json:"PASS_TO_PASS"`
	FailToFail TestResults `json:"FAIL_TO_FAIL"`
	PassToFail TestResults `json:"PASS_TO_FAIL"`
}

func (t TestsStatus) clone() TestsStatus {
	return TestsStatus{
		FailToPass: t.FailToPass.clone(), PassToPass: t.PassToPass.clone(),
		FailToFail: t.FailToFail.clone(), PassToFail: t.PassToFail.clone(),
	}
}

func (t TestsStatus) complete() bool {
	for _, tests := range [...]TestResults{t.FailToPass, t.PassToPass, t.FailToFail, t.PassToFail} {
		if tests.Success == nil || tests.Failure == nil {
			return false
		}
	}
	return true
}

// OfficialReport is a detached view of one imported harness report. In the
// pinned harness, PatchSuccessfullyApplied is set after test logs are accepted;
// it is not merely the patch command's exit status. InfraFailure and its reason
// are the case report's advice; CaseResult.Failure preserves the independently
// generated run summary's classification, which can also inspect harness logs.
type OfficialReport struct {
	PatchIsNone              bool
	PatchExists              bool
	PatchSuccessfullyApplied bool
	Resolved                 bool
	InfraFailure             bool
	InfraFailureReason       string
	TestsStatus              *TestsStatus
}

// consistent rejects field combinations the pinned harness never writes.
func (o OfficialReport) consistent() bool {
	if o.Resolved && (!o.PatchSuccessfullyApplied || o.InfraFailure) {
		return false
	}
	if o.InfraFailure && o.InfraFailureReason == "" {
		return false
	}
	return o.PatchSuccessfullyApplied == (o.TestsStatus != nil)
}

func (o OfficialReport) clone() OfficialReport {
	if o.TestsStatus != nil {
		status := o.TestsStatus.clone()
		o.TestsStatus = &status
	}
	return o
}

// CaseResult is immutable evidence for one selected task. Completed reflects
// the upstream summary's report-file-exists count: a malformed file can be
// completed and still StatusError. Only an imported, valid OfficialReport owns
// an evaluation result. Candidate and report digests refer to exact bytes.
type CaseResult struct {
	task            Task
	selectionDigest string
	runID           string
	candidateDigest string
	reportDigest    string
	emptyPatch      bool
	completed       bool
	official        *OfficialReport
	failure         Failure
	err             error
}

func (c CaseResult) Task() Task { return c.task }

// Status follows from the evidence: the imported official report owns
// resolution, and the prediction owns whether a candidate exists at all.
func (c CaseResult) Status() Status {
	switch {
	case c.official != nil && c.official.Resolved:
		return StatusResolved
	case c.official != nil:
		return StatusUnresolved
	case c.candidateDigest == "":
		return StatusMissingPrediction
	case c.emptyPatch:
		return StatusEmptyPatch
	default:
		return StatusError
	}
}
func (c CaseResult) Completed() bool         { return c.completed }
func (c CaseResult) CandidateDigest() string { return c.candidateDigest }
func (c CaseResult) ReportDigest() string    { return c.reportDigest }
func (c CaseResult) Failure() Failure        { return c.failure }
func (c CaseResult) Err() error              { return c.err }

func (c CaseResult) Official() (OfficialReport, bool) {
	if c.official == nil {
		return OfficialReport{}, false
	}
	return c.official.clone(), true
}

// GradeID binds the exact official report to its candidate, task, selection,
// harness, and attempt. It is empty when no valid official grade was imported.
func (c CaseResult) GradeID() string {
	if c.official == nil {
		return ""
	}
	// Every component is a validated fixed-width content or commit digest.
	return digest([]byte(c.runID + c.selectionDigest + c.task.Digest + HarnessRevision + c.candidateDigest + c.reportDigest))
}

// Report projects an imported official decision into eval's general contract.
// Missing predictions, empty patches, and harness errors return ErrNoGrade;
// they do not manufacture zero quality scores. A legitimate unresolved report
// has score zero even when the official harness also advises an infra failure.
// The mean of available grades is conditional on grade availability; the
// official resolution rate instead divides Summary.Resolved by Summary.Total.
func (c CaseResult) Report() (eval.Report, error) {
	if c.official == nil {
		if c.err != nil {
			return eval.Report{}, fmt.Errorf("%w: %q: %w", ErrNoGrade, c.task.InstanceID, c.err)
		}
		return eval.Report{}, fmt.Errorf("%w: %q has status %q", ErrNoGrade, c.task.InstanceID, c.Status())
	}
	parameters := metadata.Map{}
	if err := parameters.Set("harness_revision", HarnessRevision); err != nil {
		return eval.Report{}, err
	}
	metric, err := eval.NewMetric(eval.MetricConfig{Namespace: "swebench", Name: "resolved", Parameters: parameters})
	if err != nil {
		return eval.Report{}, err
	}
	verdict := eval.VerdictFail
	score := eval.Score(0)
	if c.official.Resolved {
		verdict = eval.VerdictPass
		score = eval.Score(1)
	}
	evidence := metadata.Map{}
	if err := evidence.Set("grade", struct {
		ID              string  `json:"id"`
		RunID           string  `json:"run_id"`
		SelectionDigest string  `json:"selection_digest"`
		Task            Task    `json:"task"`
		CandidateDigest string  `json:"candidate_digest"`
		ReportDigest    string  `json:"report_digest"`
		Failure         Failure `json:"failure"`
	}{c.GradeID(), c.runID, c.selectionDigest, c.task, c.candidateDigest, c.reportDigest, c.failure}); err != nil {
		return eval.Report{}, err
	}
	report := eval.Report{
		Metric: metric, Score: &score, Metadata: evidence,
		Decision: &eval.Decision{Policy: "swebench/resolved", Parameters: parameters, Verdict: verdict},
	}
	if err := report.Validate(); err != nil {
		return eval.Report{}, err
	}
	return report, nil
}

type officialReportWire struct {
	PatchIsNone              *bool        `json:"patch_is_None"`
	PatchExists              *bool        `json:"patch_exists"`
	PatchSuccessfullyApplied *bool        `json:"patch_successfully_applied"`
	Resolved                 *bool        `json:"resolved"`
	InfraFailure             *bool        `json:"infra_failure"`
	InfraFailureReason       string       `json:"infra_failure_reason,omitzero"`
	TestsStatus              *TestsStatus `json:"tests_status,omitzero"`
}

func importReport(data []byte, instanceID string) (OfficialReport, error) {
	var wire map[string]officialReportWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return OfficialReport{}, fmt.Errorf("%w: decode report %q: %w", ErrInvalidArtifacts, instanceID, err)
	}
	report, found := wire[instanceID]
	if !found || len(wire) != 1 {
		return OfficialReport{}, fmt.Errorf("%w: report must contain only instance %q", ErrArtifactMismatch, instanceID)
	}
	return report.official(instanceID)
}

func (o officialReportWire) official(instanceID string) (OfficialReport, error) {
	if o.PatchIsNone == nil || o.PatchExists == nil || o.PatchSuccessfullyApplied == nil || o.Resolved == nil || o.InfraFailure == nil {
		return OfficialReport{}, fmt.Errorf("%w: report %q omits a required boolean", ErrInvalidArtifacts, instanceID)
	}
	report := OfficialReport{
		PatchIsNone: *o.PatchIsNone, PatchExists: *o.PatchExists,
		PatchSuccessfullyApplied: *o.PatchSuccessfullyApplied, Resolved: *o.Resolved,
		InfraFailure: *o.InfraFailure, InfraFailureReason: o.InfraFailureReason,
		TestsStatus: o.TestsStatus,
	}
	if report.PatchIsNone || !report.PatchExists {
		return OfficialReport{}, fmt.Errorf("%w: report %q does not describe its non-empty prediction", ErrArtifactMismatch, instanceID)
	}
	if !report.consistent() {
		return OfficialReport{}, fmt.Errorf("%w: contradictory report fields for %q", ErrInvalidArtifacts, instanceID)
	}
	if report.TestsStatus != nil && !report.TestsStatus.complete() {
		return OfficialReport{}, fmt.Errorf("%w: report %q omits required test status arrays", ErrInvalidArtifacts, instanceID)
	}
	return report, nil
}
