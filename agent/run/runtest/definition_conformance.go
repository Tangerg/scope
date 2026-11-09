package runtest

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
)

var (
	errConformanceValuesDiffer         = errors.New("runtest: conformance values differ")
	errConformanceExecutionsShareState = errors.New("runtest: executions share mutable state")
)

// DefinitionConformanceConfig supplies representative cases, not a proof that
// arbitrary Definition code never reads hidden input or performs I/O.
type DefinitionConformanceConfig struct {
	Definition  run.Definition
	Input       run.Payload
	InitialTurn run.Turn
	// FollowingTurns exercises the original instance and each restored copy
	// through a representative multi-Step suffix.
	FollowingTurns []run.Turn
	RestoredCases  []ExecutionConformanceCase
	RejectedCases  []RejectedStepConformanceCase
}

type ExecutionConformanceCase struct {
	Name string
	// State is an exact state previously produced by the Definition.
	State          run.ExecutionState
	Turn           run.Turn
	FollowingTurns []run.Turn
}

// RejectedStepConformanceCase requires a stable domain classification instead
// of the generic execution.step.failed fallback.
type RejectedStepConformanceCase struct {
	Name string
	// State is an exact state previously produced by the Definition.
	State run.ExecutionState
	Turn  run.Turn
	// FailureKind and FailureCode are the exact persisted classification.
	FailureKind run.FailureKind
	FailureCode string
}

// RunDefinitionConformance verifies descriptor stability, concurrent Start
// isolation, exact Snapshot/Restore, byte-equivalent Step results, and stable
// rejection classifications for the supplied representative cases.
//
// All configured turns are validated before the Definition runs. Restore and
// Step inherit the test context; each Step receives a child context canceled
// when that call returns. RestoredCases and the fresh turns must
// describe successful Steps; RejectedCases describe domain violations and
// assert the exact Failure the Definition persists for them. Cancellation paths
// remain ordinary tests owned by the Definition implementation.
func RunDefinitionConformance(t *testing.T, config DefinitionConformanceConfig) {
	t.Helper()
	if err := validateDefinitionConformanceConfig(config); err != nil {
		t.Fatal(err)
	}

	t.Run("descriptor is stable", func(t *testing.T) {
		if err := verifyDescriptorStability(config.Definition); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("fresh executions are isolated and deterministic", func(t *testing.T) {
		if err := verifyFreshExecutions(t.Context(), config); err != nil {
			t.Fatal(err)
		}
	})
	for _, sample := range config.RestoredCases {
		t.Run("restored "+sample.Name, func(t *testing.T) {
			if err := verifyRestoredExecutions(t.Context(), config.Definition, sample); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, sample := range config.RejectedCases {
		t.Run("rejected "+sample.Name, func(t *testing.T) {
			if err := verifyRejectedStep(t.Context(), config.Definition, sample); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func validateDefinitionConformanceConfig(config DefinitionConformanceConfig) error {
	if lo.IsNil(config.Definition) {
		return errors.New("runtest: Definition conformance Definition is nil")
	}
	if !config.Input.Valid() {
		return errors.New("runtest: Definition conformance Input is invalid")
	}
	if err := validateConformanceTurns(config.InitialTurn, config.FollowingTurns...); err != nil {
		return fmt.Errorf("runtest: Definition conformance fresh Turn: %w", err)
	}
	restored := conformanceCaseNames{kind: "restored"}
	for index, sample := range config.RestoredCases {
		if err := restored.claim(index, sample.Name, sample.State); err != nil {
			return err
		}
		if err := validateConformanceTurns(sample.Turn, sample.FollowingTurns...); err != nil {
			return fmt.Errorf("runtest: Definition conformance restored case %q Turn: %w", sample.Name, err)
		}
	}
	rejected := conformanceCaseNames{kind: "rejected"}
	for index, sample := range config.RejectedCases {
		if err := rejected.claim(index, sample.Name, sample.State); err != nil {
			return err
		}
		if err := validateConformanceTurns(sample.Turn); err != nil {
			return fmt.Errorf("runtest: Definition conformance rejected case %q Turn: %w", sample.Name, err)
		}
		if !sample.FailureKind.Valid() || !run.ValidQualifiedName(sample.FailureCode) {
			return fmt.Errorf("runtest: Definition conformance rejected case %q must name the exact Failure kind and code", sample.Name)
		}
	}
	return nil
}

type conformanceCaseNames struct {
	kind  string
	names map[string]struct{}
}

func (c *conformanceCaseNames) claim(index int, name string, state run.ExecutionState) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("runtest: Definition conformance %s case %d has an invalid name", c.kind, index)
	}
	if _, exists := c.names[name]; exists {
		return fmt.Errorf("runtest: Definition conformance %s case name %q is duplicated", c.kind, name)
	}
	if c.names == nil {
		c.names = make(map[string]struct{})
	}
	c.names[name] = struct{}{}
	if !state.Valid() {
		return fmt.Errorf("runtest: Definition conformance %s case %q has an invalid state", c.kind, name)
	}
	return nil
}

// verifyRejectedStep restores a fresh Execution per attempt because a failed
// Step discards its instance. Repeating the attempt proves the classification
// is a function of the captured state and input rather than of one instance.
func verifyRejectedStep(
	ctx context.Context,
	definition run.Definition,
	sample RejectedStepConformanceCase,
) error {
	for attempt := range 2 {
		execution, err := callRestore(ctx, definition, sample.State)
		if err != nil {
			return err
		}
		transition, stepErr := callStep(ctx, execution, cloneTurn(sample.Turn))
		if stepErr == nil {
			return fmt.Errorf(
				"runtest: rejected case %q attempt %d returned Transition %v instead of a classified Failure",
				sample.Name, attempt, transition,
			)
		}
		failure, classified := run.StepFailure(stepErr)
		if !classified {
			return fmt.Errorf(
				"runtest: rejected case %q attempt %d returned an unclassified error, which the Runtime records as execution.step.failed: %w",
				sample.Name, attempt, stepErr,
			)
		}
		if failure.Kind() != sample.FailureKind || failure.Code() != sample.FailureCode {
			return fmt.Errorf(
				"runtest: rejected case %q attempt %d classified as %s/%s, want %s/%s",
				sample.Name, attempt,
				failure.Kind(), failure.Code(),
				sample.FailureKind, sample.FailureCode,
			)
		}
	}
	return nil
}

func validateConformanceTurns(first run.Turn, following ...run.Turn) error {
	for index, turn := range append([]run.Turn{first}, following...) {
		if !turn.Valid() {
			return fmt.Errorf("runtest: turn %d is invalid", index)
		}
	}
	return nil
}

func cloneTurn(turn run.Turn) run.Turn {
	turn.Messages = slices.Clone(turn.Messages)
	return turn
}

// callConcurrently runs call twice at once so a Definition that shares
// unsynchronized state is exposed to the race detector and to divergence.
func callConcurrently[T any](call func() (T, error)) ([2]T, error) {
	var values [2]T
	var errs [2]error
	var group sync.WaitGroup
	for index := range values {
		group.Go(func() { values[index], errs[index] = call() })
	}
	group.Wait()
	return values, errors.Join(errs[:]...)
}

func verifyDescriptorStability(definition run.Definition) error {
	encoded, err := callConcurrently(func() ([]byte, error) {
		descriptor, err := callDescriptor(definition)
		if err != nil {
			return nil, err
		}
		return jsonv2.Marshal(descriptor)
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded[0], encoded[1]) {
		return fmt.Errorf("runtest: concurrent Descriptor results differ:\nfirst:  %s\nsecond: %s", encoded[0], encoded[1])
	}
	return nil
}

func verifyFreshExecutions(ctx context.Context, config DefinitionConformanceConfig) error {
	descriptorBefore, descriptorErr := callDescriptor(config.Definition)
	if descriptorErr != nil {
		return descriptorErr
	}
	if validationErr := descriptorBefore.ValidateInput(config.Input); validationErr != nil {
		return fmt.Errorf("runtest: conformance Input does not satisfy Descriptor: %w", validationErr)
	}
	executions, err := callConcurrently(func() (run.Execution, error) {
		return callStart(config.Definition, config.Input)
	})
	if err != nil {
		return err
	}
	if pairErr := verifyExecutionPair(ctx,
		config.Definition,
		executions[0],
		executions[1],
		config.InitialTurn, config.FollowingTurns...,
	); pairErr != nil {
		return pairErr
	}
	descriptorAfter, descriptorErr := callDescriptor(config.Definition)
	if descriptorErr != nil {
		return descriptorErr
	}
	return requireEquivalent(
		"Descriptor before and after execution", descriptorBefore, descriptorAfter,
	)
}

func verifyRestoredExecutions(
	ctx context.Context,
	definition run.Definition,
	sample ExecutionConformanceCase,
) error {
	left, err := callRestore(ctx, definition, sample.State)
	if err != nil {
		return err
	}
	right, err := callRestore(ctx, definition, sample.State)
	if err != nil {
		return err
	}
	leftState, err := callSnapshot(left)
	if err != nil {
		return err
	}
	if err := requireEquivalent("restored state", sample.State, leftState); err != nil {
		return err
	}
	return verifyExecutionPair(ctx, definition, left, right, sample.Turn, sample.FollowingTurns...)
}

func verifyExecutionPair(ctx context.Context, definition run.Definition, left, right run.Execution, turn run.Turn, following ...run.Turn) error {
	turns := append([]run.Turn{turn}, following...)
	for index, batch := range turns {
		terminal, err := verifyExecutionStep(ctx, definition, left, right, batch)
		if err != nil {
			return err
		}
		if terminal && index < len(turns)-1 {
			return errors.New("runtest: a Turn follows a terminal Transition, which the Runtime never steps")
		}
	}
	return nil
}

// verifyExecutionStep reports whether the Step terminated. The Runtime never
// snapshots, restores or steps an Execution after a terminal Transition, so a
// Definition owes no state beyond it and the comparison ends there.
func verifyExecutionStep(
	ctx context.Context,
	definition run.Definition,
	left run.Execution,
	right run.Execution,
	turn run.Turn,
) (bool, error) {
	if lo.IsNil(left) || lo.IsNil(right) {
		return false, errors.New("runtest: Definition returned a nil Execution")
	}
	leftBefore, rightBefore, err := snapshotEquivalentPair("initial Execution state", left, right)
	if err != nil {
		return false, err
	}
	if restoreErr := verifyExactRestore(ctx, definition, leftBefore); restoreErr != nil {
		return false, restoreErr
	}

	restored, err := callRestore(ctx, definition, leftBefore)
	if err != nil {
		return false, err
	}
	leftTransition, err := stepIsolatedPair(ctx, left, right, rightBefore, turn)
	if err != nil {
		return false, err
	}
	restoredTransition, err := callStep(ctx, restored, cloneTurn(turn))
	if err != nil {
		return false, err
	}
	if checkErr := requireEquivalent("original versus restored Step Transition", leftTransition, restoredTransition); checkErr != nil {
		return false, checkErr
	}
	if kind := leftTransition.Kind(); kind == run.TransitionKindComplete || kind == run.TransitionKindFail {
		return true, nil
	}

	leftAfter, _, err := snapshotEquivalentPair("original versus restored resulting state", left, restored)
	if err != nil {
		return false, err
	}
	rightAfter, err := callSnapshot(right)
	if err != nil {
		return false, err
	}
	if checkErr := requireEquivalent("resulting Execution state", leftAfter, rightAfter); checkErr != nil {
		return false, checkErr
	}
	return false, verifyExactRestore(ctx, definition, leftAfter)
}

// stepIsolatedPair steps left before right so a Step that mutates state shared
// with its sibling is visible in right's snapshot before right runs.
func stepIsolatedPair(
	ctx context.Context,
	left, right run.Execution,
	rightBefore run.ExecutionState,
	turn run.Turn,
) (run.Transition, error) {
	leftTransition, err := callValidStep(ctx, left, turn)
	if err != nil {
		return run.Transition{}, err
	}
	rightStill, err := callSnapshot(right)
	if err != nil {
		return run.Transition{}, err
	}
	if comparisonErr := requireEquivalent("sibling Execution state after the first Step", rightBefore, rightStill); comparisonErr != nil {
		return run.Transition{}, fmt.Errorf("%w: %w", errConformanceExecutionsShareState, comparisonErr)
	}
	rightTransition, err := callValidStep(ctx, right, turn)
	if err != nil {
		return run.Transition{}, err
	}
	if err := requireEquivalent("Step Transition", leftTransition, rightTransition); err != nil {
		return run.Transition{}, err
	}
	return leftTransition, nil
}

func snapshotEquivalentPair(label string, left, right run.Execution) (run.ExecutionState, run.ExecutionState, error) {
	leftState, err := callSnapshot(left)
	if err != nil {
		return run.ExecutionState{}, run.ExecutionState{}, err
	}
	rightState, err := callSnapshot(right)
	if err != nil {
		return run.ExecutionState{}, run.ExecutionState{}, err
	}
	if err := requireEquivalent(label, leftState, rightState); err != nil {
		return run.ExecutionState{}, run.ExecutionState{}, err
	}
	return leftState, rightState, nil
}

func callValidStep(ctx context.Context, execution run.Execution, turn run.Turn) (run.Transition, error) {
	transition, err := callStep(ctx, execution, cloneTurn(turn))
	if err != nil {
		return run.Transition{}, err
	}
	if err := validateTransition(transition, len(turn.Messages)); err != nil {
		return run.Transition{}, err
	}
	return transition, nil
}

func verifyExactRestore(ctx context.Context, definition run.Definition, state run.ExecutionState) error {
	restored, err := callRestore(ctx, definition, state)
	if err != nil {
		return err
	}
	restoredState, err := callSnapshot(restored)
	if err != nil {
		return err
	}
	return requireEquivalent("Snapshot/Restore state", state, restoredState)
}

func validateTransition(transition run.Transition, messageCount int) error {
	if !transition.Valid() {
		return errors.New("runtest: Step returned an invalid Transition")
	}
	if uint64(transition.Consumed()) > uint64(messageCount) {
		return fmt.Errorf(
			"runtest: Step consumed %d messages from a prefix of %d",
			transition.Consumed(), messageCount,
		)
	}
	return nil
}

func requireEquivalent(label string, left, right any) error {
	leftData, err := jsonv2.Marshal(left, jsonv2.Deterministic(true))
	if err != nil {
		return fmt.Errorf("runtest: encode first %s: %w", label, err)
	}
	rightData, err := jsonv2.Marshal(right, jsonv2.Deterministic(true))
	if err != nil {
		return fmt.Errorf("runtest: encode second %s: %w", label, err)
	}
	if !bytes.Equal(leftData, rightData) {
		return fmt.Errorf("%w: %s:\nfirst:  %s\nsecond: %s", errConformanceValuesDiffer, label, leftData, rightData)
	}
	return nil
}

func callDescriptor(definition run.Definition) (descriptor run.Descriptor, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &run.CallbackPanicError{Operation: "Definition.Descriptor", Value: recovered}
		}
	}()
	descriptor = definition.Descriptor()
	if !descriptor.Valid() {
		return run.Descriptor{}, errors.New("runtest: Definition.Descriptor returned an invalid Descriptor")
	}
	return descriptor, nil
}

func callStart(definition run.Definition, input run.Payload) (execution run.Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &run.CallbackPanicError{Operation: "Definition.Start", Value: recovered}
		}
	}()
	execution, err = definition.Start(input)
	if err != nil {
		return nil, fmt.Errorf("runtest: Definition.Start: %w", err)
	}
	if lo.IsNil(execution) {
		return nil, errors.New("runtest: Definition.Start returned a nil Execution")
	}
	return execution, nil
}

func callRestore(ctx context.Context, definition run.Definition, state run.ExecutionState) (execution run.Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &run.CallbackPanicError{Operation: "Definition.Restore", Value: recovered}
		}
	}()
	execution, err = definition.Restore(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("runtest: Definition.Restore: %w", err)
	}
	if lo.IsNil(execution) {
		return nil, errors.New("runtest: Definition.Restore returned a nil Execution")
	}
	return execution, nil
}

func callStep(ctx context.Context, execution run.Execution, turn run.Turn) (transition run.Transition, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &run.CallbackPanicError{Operation: "Execution.Step", Value: recovered}
		}
	}()
	transition, err = execution.Step(ctx, turn)
	if err != nil {
		return run.Transition{}, fmt.Errorf("runtest: Execution.Step: %w", err)
	}
	return transition, nil
}

func callSnapshot(execution run.Execution) (state run.ExecutionState, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &run.CallbackPanicError{Operation: "Execution.Snapshot", Value: recovered}
		}
	}()
	state, err = execution.Snapshot()
	if err != nil {
		return run.ExecutionState{}, fmt.Errorf("runtest: Execution.Snapshot: %w", err)
	}
	if !state.Valid() {
		return run.ExecutionState{}, errors.New("runtest: Execution.Snapshot returned an invalid state")
	}
	return state, nil
}
