package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"
)

func startExecution(definition Definition, input Payload) (Execution, error) {
	execution, err := invokeCallback("Definition.Start", func() (Execution, error) {
		return definition.Start(input)
	})
	if err == nil && lo.IsNil(execution) {
		return nil, errors.New("definition.Start returned nil execution")
	}
	return execution, err
}

func restoreExecution(ctx context.Context, definition Definition, state ExecutionState) (Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	execution, err := invokeCallback("Definition.Restore", func() (Execution, error) {
		return definition.Restore(restoreContext{Context: ctx}, state)
	})
	if err == nil && lo.IsNil(execution) {
		return nil, errors.New("definition.Restore returned nil execution")
	}
	return execution, err
}

func stepExecution(ctx context.Context, execution Execution, signals []Signal) (Transition, error) {
	return invokeCallback("Execution.Step", func() (Transition, error) {
		transition, err := execution.Step(ctx, signals)
		return transition, classifyStepError(err)
	})
}

func captureExecution(execution Execution) (ExecutionState, error) {
	state, err := invokeCallback("Execution.Snapshot", execution.Snapshot)
	if err == nil && !state.Valid() {
		return ExecutionState{}, ErrInvalidExecutionState
	}
	return state, err
}

func initializeExecution(
	ctx context.Context,
	definition Definition,
	input Payload,
) (Execution, ExecutionState, Failure, error) {
	execution, err := startExecution(definition, input)
	if err != nil {
		failure := newEngineFailure(
			failureKindForError(err, FailureKindExecution), failureCodeEngineProcessStartFailed, err,
		)
		return nil, ExecutionState{}, failure, fmt.Errorf("start Execution: %w", err)
	}
	state, err := captureExecution(execution)
	if err != nil {
		failure := newEngineFailure(
			failureKindForError(err, FailureKindExecution), failureCodeEngineProcessSnapshotFailed, err,
		)
		return nil, ExecutionState{}, failure, fmt.Errorf("capture initial Execution state: %w", err)
	}
	restored, err := restoreExecution(ctx, definition, state)
	if err != nil {
		failure := newEngineFailure(
			failureKindForError(err, FailureKindExecution), failureCodeEngineProcessSnapshotUnrestorable, err,
		)
		return nil, ExecutionState{}, failure, fmt.Errorf("validate initial Execution state: %w", err)
	}
	return restored, state, Failure{}, nil
}
