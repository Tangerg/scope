package swebench

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/samber/lo"
)

// Prediction preserves the exact UTF-8 candidate patch. An empty string is an
// explicit empty submission; an absent Prediction is a missing submission.
// Trials are separate Submissions, never duplicate instance IDs in one JSONL.
type Prediction struct {
	InstanceID string
	Patch      string
}

func (p Prediction) Digest() string { return digest([]byte(p.Patch)) }

// SubmissionConfig binds one attempt to immutable selection and candidate
// values. NewSubmission snapshots Predictions; omitted cases remain missing.
type SubmissionConfig struct {
	Selection   Selection
	Model       string
	AttemptID   string
	Predictions []Prediction
}

// Submission owns one official run's identity. AttemptID identifies a new
// execution or regrade even when all candidate bytes are unchanged. Model may
// contain slashes; after the harness replaces them with __ it must be a safe
// log-directory component. The full, unmodified model name participates in the
// run identity so that the upstream directory encoding cannot alias two runs.
type Submission struct {
	selection   Selection
	model       string
	attemptID   string
	predictions []Prediction
	runID       string
}

func NewSubmission(config SubmissionConfig) (Submission, error) {
	if config.Selection.digest == "" {
		return Submission{}, fmt.Errorf("%w: initialized selection is required", ErrInvalidSubmission)
	}
	if !nonempty(config.Model) || !pathComponent(strings.ReplaceAll(config.Model, "/", "__")) || !nonempty(config.AttemptID) {
		return Submission{}, fmt.Errorf("%w: safe model and non-empty attempt ID are required", ErrInvalidSubmission)
	}
	predictions := slices.Clone(config.Predictions)
	slices.SortFunc(predictions, func(left, right Prediction) int { return strings.Compare(left.InstanceID, right.InstanceID) })
	for index, prediction := range predictions {
		if _, found := config.Selection.task(prediction.InstanceID); !found {
			return Submission{}, fmt.Errorf("%w: instance %q is outside selection", ErrInvalidSubmission, prediction.InstanceID)
		}
		if index > 0 && predictions[index-1].InstanceID == prediction.InstanceID {
			return Submission{}, fmt.Errorf("%w: duplicate prediction %q", ErrInvalidSubmission, prediction.InstanceID)
		}
	}
	type identityPrediction struct {
		InstanceID string `json:"instance_id"`
		Patch      string `json:"model_patch"`
	}
	identityPredictions := make([]identityPrediction, len(predictions))
	for index, prediction := range predictions {
		identityPredictions[index] = identityPrediction(prediction)
	}
	identity, err := jsonv2.Marshal(struct {
		Selection       string               `json:"selection"`
		HarnessRevision string               `json:"harness_revision"`
		Model           string               `json:"model"`
		AttemptID       string               `json:"attempt_id"`
		Predictions     []identityPrediction `json:"predictions"`
	}{config.Selection.digest, HarnessRevision, config.Model, config.AttemptID, identityPredictions})
	if err != nil {
		return Submission{}, fmt.Errorf("%w: encode identity: %w", ErrInvalidSubmission, err)
	}
	return Submission{
		selection: config.Selection, model: config.Model, attemptID: config.AttemptID,
		predictions: predictions, runID: strings.TrimPrefix(digest(identity), "sha256:"),
	}, nil
}

func (s Submission) Selection() Selection      { return s.selection }
func (s Submission) Model() string             { return s.model }
func (s Submission) AttemptID() string         { return s.attemptID }
func (s Submission) RunID() string             { return s.runID }
func (s Submission) Predictions() []Prediction { return slices.Clone(s.predictions) }

// WritePredictions encodes every row of the official JSONL before writing any
// bytes and never normalizes patch whitespace. An I/O failure can still leave
// partial output; the Host owns atomic publication.
func (s Submission) WritePredictions(writer io.Writer) error {
	if s.runID == "" || lo.IsNil(writer) {
		return fmt.Errorf("%w: initialized submission and writer are required", ErrInvalidSubmission)
	}
	var buffer bytes.Buffer
	for _, prediction := range s.predictions {
		line, err := jsonv2.Marshal(struct {
			InstanceID string `json:"instance_id"`
			Model      string `json:"model_name_or_path"`
			Patch      string `json:"model_patch"`
		}{prediction.InstanceID, s.model, prediction.Patch})
		if err != nil {
			return fmt.Errorf("%w: encode %q: %w", ErrInvalidSubmission, prediction.InstanceID, err)
		}
		buffer.Write(line)
		buffer.WriteByte('\n')
	}
	if _, err := buffer.WriteTo(writer); err != nil {
		return fmt.Errorf("eval/swebench: write predictions: %w", err)
	}
	return nil
}

// Arguments returns the official harness module and selection flags as separate
// process arguments, never shell text. datasetPath must name a frozen dataset
// matching Selection and predictionsPath must hold WritePredictions output.
func (s Submission) Arguments(datasetPath, predictionsPath string) ([]string, error) {
	if s.runID == "" || !nonempty(datasetPath) || !nonempty(predictionsPath) {
		return nil, fmt.Errorf("%w: initialized submission and dataset/prediction paths are required", ErrInvalidSubmission)
	}
	arguments := []string{
		"-m", "swebench.harness.run_evaluation", "--dataset_name", datasetPath,
		"--split", s.selection.split, "--predictions_path", predictionsPath,
		"--run_id", s.runID, "--instance_ids",
	}
	for _, task := range s.selection.tasks {
		arguments = append(arguments, task.InstanceID)
	}
	return arguments, nil
}

// Collect imports a completed official run from a trusted, immutable snapshot
// of the harness working directory. Confine disk reads with os.OpenRoot and
// Root.FS; os.DirFS alone does not prevent symlink escapes.
//
// Collect requires one results.json covering the entire Selection; for cases
// run separately, invoke the official make_run_report once over all of them.
// Missing or malformed case reports remain StatusError without a grade, while
// a candidate mismatch, an unbound report, or an inconsistent summary rejects
// the collection. Matching artifacts do not prove what the harness executed.
func (s Submission) Collect(artifacts fs.FS) (Collection, error) {
	if s.runID == "" || lo.IsNil(artifacts) {
		return Collection{}, fmt.Errorf("%w: initialized submission and artifact filesystem are required", ErrInvalidArtifacts)
	}
	results := make([]CaseResult, 0, s.selection.Len())
	for _, task := range s.selection.tasks {
		result, err := s.collectCase(artifacts, task)
		if err != nil {
			return Collection{}, err
		}
		results = append(results, result)
	}
	summaryData, err := fs.ReadFile(artifacts, path.Join("logs", "evaluation", s.runID, "results.json"))
	if err != nil {
		return Collection{}, fmt.Errorf("%w: read official run summary: %w", ErrInvalidArtifacts, err)
	}
	summary, err := importSummary(summaryData, results)
	if err != nil {
		return Collection{}, err
	}
	return Collection{results: results, summary: summary}, nil
}

func (s Submission) collectCase(artifacts fs.FS, task Task) (CaseResult, error) {
	result := CaseResult{task: task, selectionDigest: s.selection.digest, runID: s.runID, status: StatusMissingPrediction}
	index, found := slices.BinarySearchFunc(s.predictions, task.InstanceID, func(prediction Prediction, id string) int {
		return strings.Compare(prediction.InstanceID, id)
	})
	if !found {
		return result, nil
	}
	prediction := s.predictions[index]
	result.candidateDigest = prediction.Digest()
	if prediction.Patch == "" {
		result.status = StatusEmptyPatch
		return result, nil
	}
	result.status = StatusError
	directory := path.Join("logs", "evaluation", s.runID, strings.ReplaceAll(s.model, "/", "__"), task.InstanceID)
	reportData, reportErr := fs.ReadFile(artifacts, path.Join(directory, "report.json"))
	result.completed = reportErr == nil
	patch, patchErr := fs.ReadFile(artifacts, path.Join(directory, "patch.diff"))
	if patchErr != nil && !errors.Is(patchErr, fs.ErrNotExist) {
		return CaseResult{}, fmt.Errorf("%w: read candidate %q: %w", ErrInvalidArtifacts, task.InstanceID, patchErr)
	}
	if patchErr == nil && digest(patch) != result.candidateDigest {
		return CaseResult{}, fmt.Errorf("%w: candidate digest mismatch for %q", ErrArtifactMismatch, task.InstanceID)
	}
	if reportErr != nil {
		if !errors.Is(reportErr, fs.ErrNotExist) {
			return CaseResult{}, fmt.Errorf("%w: read report %q: %w", ErrInvalidArtifacts, task.InstanceID, reportErr)
		}
		result.err = fmt.Errorf("%w: %q report unavailable: %w", ErrNoGrade, task.InstanceID, reportErr)
		return result, nil
	}
	if patchErr != nil {
		return CaseResult{}, fmt.Errorf("%w: report for %q has no candidate artifact: %w", ErrArtifactMismatch, task.InstanceID, patchErr)
	}
	result.reportDigest = digest(reportData)
	report, err := importReport(reportData, task.InstanceID)
	if err != nil {
		if errors.Is(err, ErrArtifactMismatch) {
			return CaseResult{}, err
		}
		result.err = err
		return result, nil
	}
	result.official = &report
	result.status = StatusUnresolved
	if report.Resolved {
		result.status = StatusResolved
	}
	return result, nil
}
