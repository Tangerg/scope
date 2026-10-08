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
	groups := groupCases(results)
	if err := wire.reconcile(groups); err != nil {
		return Summary{}, err
	}
	if err := wire.classifyFailures(results); err != nil {
		return Summary{}, err
	}
	if wire.UnstoppedInstances != nil && *wire.UnstoppedInstances != len(wire.UnstoppedContainers) {
		return Summary{}, fmt.Errorf("%w: official unstopped container count is inconsistent", ErrInvalidArtifacts)
	}
	return Summary{
		Total: len(results), Submitted: len(groups.submitted), Completed: len(groups.completed),
		Resolved: len(groups.resolved), Unresolved: len(groups.unresolved), MissingPredictions: len(groups.missing),
		EmptyPatches: len(groups.empty), Errors: len(groups.failed),
		InfrastructureFailures: len(wire.InfraFailureIDs), AmbiguousFailures: len(wire.AmbiguousFailureIDs),
	}, nil
}

// caseGroups lists instance IDs per official summary field in Selection order,
// which sameIDs relies on being sorted.
type caseGroups struct {
	submitted, completed, resolved, unresolved, missing, empty, failed []string
}

func groupCases(results []CaseResult) caseGroups {
	var groups caseGroups
	for _, result := range results {
		id := result.task.InstanceID
		if result.Status() != StatusMissingPrediction {
			groups.submitted = append(groups.submitted, id)
		}
		if result.completed {
			groups.completed = append(groups.completed, id)
		}
		switch result.Status() {
		case StatusMissingPrediction:
			groups.missing = append(groups.missing, id)
		case StatusEmptyPatch:
			groups.empty = append(groups.empty, id)
		case StatusError:
			groups.failed = append(groups.failed, id)
		case StatusResolved:
			groups.resolved = append(groups.resolved, id)
		case StatusUnresolved:
			groups.unresolved = append(groups.unresolved, id)
		}
	}
	return groups
}

func (o officialSummaryWire) reconcile(groups caseGroups) error {
	for _, check := range []struct {
		name     string
		count    *int
		ids      []string
		expected []string
	}{
		{"submitted", o.SubmittedInstances, o.SubmittedIDs, groups.submitted},
		{"completed", o.CompletedInstances, o.CompletedIDs, groups.completed},
		{"resolved", o.ResolvedInstances, o.ResolvedIDs, groups.resolved},
		{"unresolved", o.UnresolvedInstances, o.UnresolvedIDs, groups.unresolved},
		{"empty_patch", o.EmptyPatchInstances, o.EmptyPatchIDs, groups.empty},
		{"error", o.ErrorInstances, o.ErrorIDs, groups.failed},
	} {
		if check.count == nil || *check.count != len(check.expected) || !sameIDs(check.ids, check.expected) {
			return fmt.Errorf("%w: official %s count or IDs disagree with imported artifacts", ErrInvalidArtifacts, check.name)
		}
	}
	if !sameIDs(o.IncompleteIDs, groups.missing) {
		return fmt.Errorf("%w: official incomplete IDs disagree with missing predictions", ErrInvalidArtifacts)
	}
	return nil
}

func (o officialSummaryWire) classifyFailures(results []CaseResult) error {
	if !countedIDs(o.InfraFailureInstances, o.InfraFailureIDs) || !countedIDs(o.AmbiguousFailureInstances, o.AmbiguousFailureIDs) {
		return fmt.Errorf("%w: official diagnostic counts or IDs are invalid", ErrInvalidArtifacts)
	}
	classified := make(map[string]struct{}, len(o.InfraFailureIDs)+len(o.AmbiguousFailureIDs))
	for _, group := range []struct {
		kind FailureKind
		ids  []string
	}{
		{FailureInfrastructure, o.InfraFailureIDs},
		{FailureAmbiguous, o.AmbiguousFailureIDs},
	} {
		for _, id := range group.ids {
			index, found := slices.BinarySearchFunc(results, id, func(result CaseResult, instanceID string) int {
				return strings.Compare(result.task.InstanceID, instanceID)
			})
			_, duplicate := classified[id]
			if !found || duplicate || !results[index].Status().classifiable() || !nonempty(o.FailureReasons[id]) {
				return fmt.Errorf("%w: official failure classification does not belong to an unresolved or error instance %q", ErrInvalidArtifacts, id)
			}
			classified[id] = struct{}{}
			results[index].failure = Failure{Kind: group.kind, Reason: o.FailureReasons[id]}
		}
	}
	if len(classified) != len(o.FailureReasons) {
		return fmt.Errorf("%w: official failure reasons contain unclassified instances", ErrInvalidArtifacts)
	}
	return nil
}

func countedIDs(count *int, ids []string) bool {
	return count != nil && ids != nil && *count == len(ids)
}

func sameIDs(actual, expected []string) bool {
	if actual == nil {
		return false
	}
	ordered := slices.Clone(actual)
	slices.Sort(ordered)
	return slices.Equal(ordered, expected)
}
