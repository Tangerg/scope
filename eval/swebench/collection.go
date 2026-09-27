package swebench

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
)

// Summary preserves official counts for the complete frozen selection.
// InfrastructureFailures and AmbiguousFailures overlap Unresolved or Errors;
// neither removes tasks from Total. Completed can include malformed reports.
// This is a validated import of results.json, not a replacement benchmark score.
type Summary struct {
	Total                  int
	Submitted              int
	Completed              int
	Resolved               int
	Unresolved             int
	MissingPredictions     int
	EmptyPatches           int
	Errors                 int
	InfrastructureFailures int
	AmbiguousFailures      int
}

// Collection owns results in the Selection's stable instance order. It can be
// constructed only by importing and reconciling the official complete run.
type Collection struct {
	results []CaseResult
	summary Summary
}

func (c Collection) Cases() []CaseResult { return slices.Clone(c.results) }
func (c Collection) Summary() Summary    { return c.summary }

// This is the upstream wire schema, not a Scope persistence version. A harness
// protocol change replaces this adapter instead of adding version dispatch.
const officialSummarySchema = 2

type officialSummaryWire struct {
	TotalInstances            *int              `json:"total_instances"`
	SubmittedInstances        *int              `json:"submitted_instances"`
	CompletedInstances        *int              `json:"completed_instances"`
	ResolvedInstances         *int              `json:"resolved_instances"`
	UnresolvedInstances       *int              `json:"unresolved_instances"`
	InfraFailureInstances     *int              `json:"infra_failure_instances"`
	AmbiguousFailureInstances *int              `json:"ambiguous_failure_instances"`
	EmptyPatchInstances       *int              `json:"empty_patch_instances"`
	ErrorInstances            *int              `json:"error_instances"`
	CompletedIDs              []string          `json:"completed_ids"`
	IncompleteIDs             []string          `json:"incomplete_ids"`
	EmptyPatchIDs             []string          `json:"empty_patch_ids"`
	SubmittedIDs              []string          `json:"submitted_ids"`
	ResolvedIDs               []string          `json:"resolved_ids"`
	UnresolvedIDs             []string          `json:"unresolved_ids"`
	InfraFailureIDs           []string          `json:"infra_failure_ids"`
	AmbiguousFailureIDs       []string          `json:"ambiguous_failure_ids"`
	FailureReasons            map[string]string `json:"failure_reasons"`
	ErrorIDs                  []string          `json:"error_ids"`
	SchemaVersion             int               `json:"schema_version"`
	UnstoppedInstances        *int              `json:"unstopped_instances,omitzero"`
	UnstoppedContainers       []string          `json:"unstopped_containers,omitzero"`
	UnremovedImages           []string          `json:"unremoved_images,omitzero"`
}

func importSummary(data []byte, results []CaseResult) (Summary, error) {
	var wire officialSummaryWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return Summary{}, fmt.Errorf("%w: decode official run summary: %w", ErrInvalidArtifacts, err)
	}
	if wire.SchemaVersion != officialSummarySchema || wire.TotalInstances == nil || *wire.TotalInstances != len(results) || wire.FailureReasons == nil {
		return Summary{}, fmt.Errorf("%w: official summary schema, total, or failure reasons do not match the frozen run", ErrInvalidArtifacts)
	}
	var submitted, completed, resolved, unresolved, missing, empty, failed []string
	for _, result := range results {
		id := result.task.InstanceID
		if result.status != StatusMissingPrediction {
			submitted = append(submitted, id)
		}
		if result.completed {
			completed = append(completed, id)
		}
		switch result.status {
		case StatusMissingPrediction:
			missing = append(missing, id)
		case StatusEmptyPatch:
			empty = append(empty, id)
		case StatusError:
			failed = append(failed, id)
		case StatusResolved:
			resolved = append(resolved, id)
		case StatusUnresolved:
			unresolved = append(unresolved, id)
		}
	}
	for _, check := range []struct {
		name     string
		count    *int
		ids      []string
		expected []string
	}{
		{"submitted", wire.SubmittedInstances, wire.SubmittedIDs, submitted},
		{"completed", wire.CompletedInstances, wire.CompletedIDs, completed},
		{"resolved", wire.ResolvedInstances, wire.ResolvedIDs, resolved},
		{"unresolved", wire.UnresolvedInstances, wire.UnresolvedIDs, unresolved},
		{"empty_patch", wire.EmptyPatchInstances, wire.EmptyPatchIDs, empty},
		{"error", wire.ErrorInstances, wire.ErrorIDs, failed},
	} {
		if check.count == nil || *check.count != len(check.expected) || !sameIDs(check.ids, check.expected) {
			return Summary{}, fmt.Errorf("%w: official %s count or IDs disagree with imported artifacts", ErrInvalidArtifacts, check.name)
		}
	}
	if !sameIDs(wire.IncompleteIDs, missing) {
		return Summary{}, fmt.Errorf("%w: official incomplete IDs disagree with missing predictions", ErrInvalidArtifacts)
	}
	if wire.InfraFailureInstances == nil || *wire.InfraFailureInstances != len(wire.InfraFailureIDs) ||
		wire.AmbiguousFailureInstances == nil || *wire.AmbiguousFailureInstances != len(wire.AmbiguousFailureIDs) ||
		wire.InfraFailureIDs == nil || wire.AmbiguousFailureIDs == nil {
		return Summary{}, fmt.Errorf("%w: official diagnostic counts or IDs are invalid", ErrInvalidArtifacts)
	}
	classified := make(map[string]struct{}, len(wire.InfraFailureIDs)+len(wire.AmbiguousFailureIDs))
	for _, group := range []struct {
		kind FailureKind
		ids  []string
	}{
		{FailureInfrastructure, wire.InfraFailureIDs},
		{FailureAmbiguous, wire.AmbiguousFailureIDs},
	} {
		for _, id := range group.ids {
			index, found := slices.BinarySearchFunc(results, id, func(result CaseResult, instanceID string) int {
				return strings.Compare(result.task.InstanceID, instanceID)
			})
			_, duplicate := classified[id]
			if !found || duplicate || (results[index].status != StatusError && results[index].status != StatusUnresolved) || !nonempty(wire.FailureReasons[id]) {
				return Summary{}, fmt.Errorf("%w: official failure classification does not belong to an unresolved or error instance %q", ErrInvalidArtifacts, id)
			}
			classified[id] = struct{}{}
			results[index].failure = Failure{Kind: group.kind, Reason: wire.FailureReasons[id]}
		}
	}
	if len(classified) != len(wire.FailureReasons) {
		return Summary{}, fmt.Errorf("%w: official failure reasons contain unclassified instances", ErrInvalidArtifacts)
	}
	if wire.UnstoppedInstances != nil && *wire.UnstoppedInstances != len(wire.UnstoppedContainers) {
		return Summary{}, fmt.Errorf("%w: official unstopped container count is inconsistent", ErrInvalidArtifacts)
	}
	return Summary{
		Total: len(results), Submitted: len(submitted), Completed: len(completed),
		Resolved: len(resolved), Unresolved: len(unresolved), MissingPredictions: len(missing),
		EmptyPatches: len(empty), Errors: len(failed),
		InfrastructureFailures: len(wire.InfraFailureIDs), AmbiguousFailures: len(wire.AmbiguousFailureIDs),
	}, nil
}

func sameIDs(actual, expected []string) bool {
	if actual == nil {
		return false
	}
	ordered := slices.Clone(actual)
	slices.Sort(ordered)
	return slices.Equal(ordered, expected)
}
