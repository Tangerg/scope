package agent

import (
	"cmp"
	"errors"
	"slices"
)

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

func newTreeRuntimeFailure(cause error) Failure {
	kind := FailureKindExternal
	code := failureCodeEngineTreeDurabilityFailed
	switch {
	case errors.Is(cause, ErrResourceLimitExceeded):
		kind, code = FailureKindExecution, failureCodeEngineLimitSnapshot
	case errors.Is(cause, ErrDurabilityConflict):
		kind = FailureKindContract
		code = failureCodeEngineTreeDurabilityConflict
	case errors.Is(cause, ErrTreeIncarnationConflict):
		code = failureCodeEngineTreeIncarnationConflict
	}
	return newEngineFailure(kind, code, cause)
}
