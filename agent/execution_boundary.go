package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"
)

const (
	processStartFailedCode          = "engine.process.start.failed"
	processSnapshotFailedCode       = "engine.process.snapshot.failed"
	processSnapshotUnrestorableCode = "engine.process.snapshot.unrestorable"
)

func failureKindForError(err error) FailureKind {
	if _, ok := errors.AsType[executionPanicError](err); ok {
		return FailureKindPanic
	}
	return FailureKindExecution
}

type executionPanicError struct{ value any }

func (e executionPanicError) Error() string {
	return fmt.Sprintf("execution panicked: %v", e.value)
}

func startExecution(definition Definition, input Input) (execution Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			execution = nil
			err = executionPanicError{value: recovered}
		}
	}()
	execution, err = definition.Start(input)
	if err == nil && lo.IsNil(execution) {
		return nil, errors.New("definition.Start returned nil execution")
	}
	return execution, err
}

func restoreExecution(definition Definition, state ExecutionState) (execution Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			execution = nil
			err = executionPanicError{value: recovered}
		}
	}()
	execution, err = definition.Restore(state)
	if err == nil && lo.IsNil(execution) {
		return nil, errors.New("definition.Restore returned nil execution")
	}
	return execution, err
}

func stepExecution(ctx context.Context, execution Execution, signals []Signal) (transition Transition, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			transition = Transition{}
			err = executionPanicError{value: recovered}
		}
	}()
	return execution.Step(ctx, signals)
}

func captureExecution(execution Execution) (state ExecutionState, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			state = ExecutionState{}
			err = executionPanicError{value: recovered}
		}
	}()
	state, err = execution.Snapshot()
	if err == nil && !state.Valid() {
		return ExecutionState{}, ErrInvalidExecutionState
	}
	return state, err
}

func initializeExecution(
	definition Definition,
	input Input,
) (Execution, ExecutionState, Failure, error) {
	execution, err := startExecution(definition, input)
	if err != nil {
		failure := newEngineFailure(
			failureKindForError(err), processStartFailedCode, err,
		)
		return nil, ExecutionState{}, failure, fmt.Errorf("start Execution: %w", err)
	}
	state, err := captureExecution(execution)
	if err != nil {
		failure := newEngineFailure(
			failureKindForError(err), processSnapshotFailedCode, err,
		)
		return nil, ExecutionState{}, failure, fmt.Errorf("capture initial Execution state: %w", err)
	}
	restored, err := restoreExecution(definition, state)
	if err != nil {
		failure := newEngineFailure(
			failureKindForError(err), processSnapshotUnrestorableCode, err,
		)
		return nil, ExecutionState{}, failure, fmt.Errorf("validate initial Execution state: %w", err)
	}
	return restored, state, Failure{}, nil
}
