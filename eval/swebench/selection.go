// Package swebench exchanges predictions and reports with the official
// SWE-bench harness. It does not execute candidates, parse test logs, or grade
// patches. The Host supplies a frozen dataset, runs the pinned harness, and
// grants read access to its completed artifacts.
//
// NewSelection freezes dataset content identity and the exact denominator.
// NewSubmission binds that selection, an explicit attempt, and exact candidate
// bytes to a content-derived RunID. Export predictions with WritePredictions,
// run the official harness using that RunID and the selected instance IDs, then
// import the complete run with Collect. A regrade requires a new AttemptID.
//
// Task digests must cover the complete task and grading environment contract:
// problem input, base commit, test patch, expected tests, parser, evaluation
// script, consumed image digest and platform, and execution policy. Keep gold
// patches, hidden tests, and grading assets out of the candidate's input. These
// identities bind artifacts; this package cannot attest what a container ran.
// The Host must make the actual harness consume pinned images and dependencies.
package swebench

import (
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// HarnessRevision identifies the upstream protocol implemented by this package.
// Its report and cache behavior are defined in swebench/harness/{grading,
// reporting,run_evaluation}.py in github.com/SWE-bench/SWE-bench at this commit.
const HarnessRevision = "02e7a74ffd0b707aab73d203fe87bdc7c76afc8e"

var (
	ErrInvalidSelection  = errors.New("eval/swebench: invalid selection")
	ErrInvalidSubmission = errors.New("eval/swebench: invalid submission")
	ErrInvalidArtifacts  = errors.New("eval/swebench: invalid artifacts")
	ErrArtifactMismatch  = errors.New("eval/swebench: artifact identity mismatch")
	ErrNoGrade           = errors.New("eval/swebench: no official grade")
)

// Task binds an official instance ID to the SHA-256 digest of its complete
// task and grading contract, written as sha256:<lowercase hex>.
type Task struct {
	InstanceID string `json:"instance_id"`
	Digest     string `json:"digest"`
}

// SelectionConfig identifies content independently from an upstream wire
// schema version. Revision is an immutable 40- or 64-digit hexadecimal commit
// or content revision; names such as main and latest cannot freeze a dataset.
type SelectionConfig struct {
	Dataset  string
	Revision string
	Split    string
	Tasks    []Task
}

// Selection is an immutable set ordered by instance ID. Caller mutations of
// the constructor input or Tasks result cannot change its identity.
type Selection struct {
	dataset  string
	revision string
	split    string
	tasks    []Task
	digest   string
}

func NewSelection(config SelectionConfig) (Selection, error) {
	if !nonempty(config.Dataset) || !nonempty(config.Split) {
		return Selection{}, fmt.Errorf("%w: dataset and split are required without surrounding whitespace", ErrInvalidSelection)
	}
	if !hexadecimal(config.Revision, 40) && !hexadecimal(config.Revision, 64) {
		return Selection{}, fmt.Errorf("%w: revision must be an immutable hexadecimal commit or content revision", ErrInvalidSelection)
	}
	if len(config.Tasks) == 0 {
		return Selection{}, fmt.Errorf("%w: at least one task is required", ErrInvalidSelection)
	}
	tasks := slices.Clone(config.Tasks)
	slices.SortFunc(tasks, func(left, right Task) int { return strings.Compare(left.InstanceID, right.InstanceID) })
	for index, task := range tasks {
		if !pathComponent(task.InstanceID) || !contentDigest(task.Digest) {
			return Selection{}, fmt.Errorf("%w: task %q requires a safe instance ID and SHA-256 digest", ErrInvalidSelection, task.InstanceID)
		}
		if index > 0 && tasks[index-1].InstanceID == task.InstanceID {
			return Selection{}, fmt.Errorf("%w: duplicate instance %q", ErrInvalidSelection, task.InstanceID)
		}
	}
	identity, err := jsonv2.Marshal(struct {
		Dataset  string `json:"dataset"`
		Revision string `json:"revision"`
		Split    string `json:"split"`
		Tasks    []Task `json:"tasks"`
	}{config.Dataset, config.Revision, config.Split, tasks})
	if err != nil {
		return Selection{}, fmt.Errorf("%w: encode identity: %w", ErrInvalidSelection, err)
	}
	return Selection{dataset: config.Dataset, revision: config.Revision, split: config.Split, tasks: tasks, digest: digest(identity)}, nil
}

func (s Selection) Dataset() string  { return s.dataset }
func (s Selection) Revision() string { return s.revision }
func (s Selection) Split() string    { return s.split }
func (s Selection) Tasks() []Task    { return slices.Clone(s.tasks) }
func (s Selection) Digest() string   { return s.digest }
func (s Selection) Len() int         { return len(s.tasks) }

func (s Selection) task(instanceID string) (Task, bool) {
	index, found := slices.BinarySearchFunc(s.tasks, instanceID, func(task Task, id string) int {
		return strings.Compare(task.InstanceID, id)
	})
	if !found {
		return Task{}, false
	}
	return s.tasks[index], true
}

func nonempty(value string) bool { return value != "" && value == strings.TrimSpace(value) }

func pathComponent(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func hexadecimal(value string, size int) bool {
	if len(value) != size || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func contentDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && hexadecimal(strings.TrimPrefix(value, "sha256:"), 64)
}

func digest(value []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(value)) }
