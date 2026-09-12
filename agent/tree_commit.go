package agent

import (
	"cmp"
	"encoding/json"
	"errors"
	"slices"
)

const (
	treeDurabilityConflictCode  = "engine.tree.durability_conflict"
	treeDurabilityFailureCode   = "engine.tree.durability_failed"
	treeIncarnationConflictCode = "engine.tree.incarnation_conflict"
)

func terminalEventPayload(process *processState) json.RawMessage {
	usage := process.usage
	eventPayload := processFinishedEventPayload{
		ProcessStatus:    process.status,
		TerminationCause: process.termination.Cause(),
		Usage:            &usage,
	}
	if failure, failed := process.termination.Failure(); failed {
		eventPayload.FailureKind = failure.Kind()
		eventPayload.FailureCode = failure.Code()
	}
	payload, _ := json.Marshal(eventPayload)
	return payload
}

func orderedProcesses(values map[ProcessID]*processState) []*processState {
	processes := make([]*processState, 0, len(values))
	for _, process := range values {
		processes = append(processes, process)
	}
	slices.SortFunc(processes, func(left, right *processState) int {
		if order := cmp.Compare(
			left.handle.relation.Depth(),
			right.handle.relation.Depth(),
		); order != 0 {
			return order
		}
		return cmp.Compare(
			left.handle.processID.String(),
			right.handle.processID.String(),
		)
	})
	return processes
}

func newTreeDurabilityFailure(cause error) Failure {
	kind := FailureKindExternal
	code := treeDurabilityFailureCode
	switch {
	case errors.Is(cause, ErrDurabilityConflict):
		kind = FailureKindContract
		code = treeDurabilityConflictCode
	case errors.Is(cause, ErrTreeIncarnationConflict):
		code = treeIncarnationConflictCode
	}
	return newEngineFailure(kind, code, cause)
}
