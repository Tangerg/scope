package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/samber/lo"
)

func failureFromError(kind FailureKind, code string, err error) (Failure, error) {
	message := "unknown error"
	if err != nil {
		// Go errors can contain arbitrary bytes; persisted diagnostics must
		// preserve their value when strict JSON decoding runs during recovery.
		message = strings.TrimSpace(strings.ToValidUTF8(err.Error(), "\ufffd"))
	}
	if message == "" {
		message = "unknown error"
	}
	if len(message) > maxFailureMessageBytes {
		message = message[:maxFailureMessageBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message = strings.TrimSpace(message)
	}
	return NewFailure(kind, code, message)
}

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
		failure := processInitializationFailure(
			failureKindForError(err), "engine.process.start.failed", err,
		)
		return nil, ExecutionState{}, failure, fmt.Errorf("start Execution: %w", err)
	}
	state, err := captureExecution(execution)
	if err != nil {
		failure := processInitializationFailure(
			failureKindForError(err), "engine.process.snapshot.failed", err,
		)
		return nil, ExecutionState{}, failure, fmt.Errorf("capture initial Execution state: %w", err)
	}
	restored, err := restoreExecution(definition, state)
	if err != nil {
		failure := processInitializationFailure(
			failureKindForError(err), "engine.process.snapshot.unrestorable", err,
		)
		return nil, ExecutionState{}, failure, fmt.Errorf("validate initial Execution state: %w", err)
	}
	return restored, state, Failure{}, nil
}

func processInitializationFailure(kind FailureKind, code string, cause error) Failure {
	failure, err := failureFromError(kind, code, cause)
	if err == nil {
		return failure
	}
	failure, _ = NewFailure(
		FailureKindContract,
		"engine.process.start_outcome.invalid",
		"invalid Process initialization failure",
	)
	return failure
}
