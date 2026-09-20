package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"
)

func startExecution(definition Definition, input Payload) (execution Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			execution = nil
			err = callbackPanic("Definition.Start", recovered)
		}
	}()
	execution, err = definition.Start(input)
	if err == nil && lo.IsNil(execution) {
		return nil, errors.New("definition.Start returned nil execution")
	}
	return execution, sealCallbackError(err)
}

func restoreExecution(ctx context.Context, definition Definition, state ExecutionState) (execution Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			execution = nil
			err = callbackPanic("Definition.Restore", recovered)
		}
	}()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	execution, err = definition.Restore(restoreContext{Context: ctx}, state)
	if err == nil && lo.IsNil(execution) {
		return nil, errors.New("definition.Restore returned nil execution")
	}
	return execution, sealCallbackError(err)
}

func stepExecution(ctx context.Context, execution Execution, signals []Signal) (transition Transition, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			transition = Transition{}
			err = callbackPanic("Execution.Step", recovered)
		}
	}()
	transition, err = execution.Step(ctx, signals)
	return transition, sealCallbackError(err)
}

func captureExecution(execution Execution) (state ExecutionState, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			state = ExecutionState{}
			err = callbackPanic("Execution.Snapshot", recovered)
		}
	}()
	state, err = execution.Snapshot()
	if err == nil && !state.Valid() {
		return ExecutionState{}, ErrInvalidExecutionState
	}
	return state, sealCallbackError(err)
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
