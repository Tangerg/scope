package agenttest

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

	agent "github.com/Tangerg/scope/agent"
)

var (
	errConformanceValuesDiffer         = errors.New("agenttest: conformance values differ")
	errConformanceExecutionsShareState = errors.New("agenttest: executions share mutable state")
)

// DefinitionConformanceConfig supplies representative cases, not a proof that
// arbitrary Definition code never reads hidden input or performs I/O.
type DefinitionConformanceConfig struct {
	Definition     agent.Definition
	Input          agent.Payload
	InitialSignals []agent.Signal
	// FollowingSignals exercises the original instance and each restored copy
	// through a representative multi-Step suffix.
	FollowingSignals [][]agent.Signal
	RestoredCases    []ExecutionConformanceCase
	RejectedCases    []RejectedStepConformanceCase
}

type ExecutionConformanceCase struct {
	Name string
	// State is an exact state previously produced by the Definition.
	State            agent.ExecutionState
	Signals          []agent.Signal
	FollowingSignals [][]agent.Signal
}

// RejectedStepConformanceCase requires a stable domain classification instead
// of the generic execution.step.failed fallback.
type RejectedStepConformanceCase struct {
	Name string
	// State is an exact state previously produced by the Definition.
	State   agent.ExecutionState
	Signals []agent.Signal
	// FailureKind and FailureCode are the exact persisted classification.
	FailureKind agent.FailureKind
	FailureCode string
}

// RunDefinitionConformance verifies descriptor stability, concurrent Start
// isolation, exact Snapshot/Restore, byte-equivalent Step results, and stable
// rejection classifications for the supplied representative cases.
//
// All configured Signals are validated before the Definition runs. Restore and
// Step inherit the test context; each Step receives a child context canceled
// when that call returns. RestoredCases and the fresh Signal batches must
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
		return errors.New("agenttest: Definition conformance Definition is nil")
	}
	if !config.Input.Valid() {
		return errors.New("agenttest: Definition conformance Input is invalid")
	}
	if err := validateConformanceSignals(config.InitialSignals, config.FollowingSignals...); err != nil {
		return fmt.Errorf("agenttest: Definition conformance fresh Signals: %w", err)
	}
	restored := conformanceCaseNames{kind: "restored"}
	for index, sample := range config.RestoredCases {
		if err := restored.claim(index, sample.Name, sample.State); err != nil {
			return err
		}
		if err := validateConformanceSignals(sample.Signals, sample.FollowingSignals...); err != nil {
			return fmt.Errorf("agenttest: Definition conformance restored case %q Signals: %w", sample.Name, err)
		}
	}
	rejected := conformanceCaseNames{kind: "rejected"}
	for index, sample := range config.RejectedCases {
		if err := rejected.claim(index, sample.Name, sample.State); err != nil {
			return err
		}
		if err := validateConformanceSignals(sample.Signals); err != nil {
			return fmt.Errorf("agenttest: Definition conformance rejected case %q Signals: %w", sample.Name, err)
		}
		if !sample.FailureKind.Valid() || !agent.ValidQualifiedName(sample.FailureCode) {
			return fmt.Errorf("agenttest: Definition conformance rejected case %q must name the exact Failure kind and code", sample.Name)
		}
	}
	return nil
}

// conformanceCaseNames keeps case names unique per kind because each name
// becomes a subtest name and must identify one captured state.
type conformanceCaseNames struct {
	kind  string
	names map[string]struct{}
}

func (c *conformanceCaseNames) claim(index int, name string, state agent.ExecutionState) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("agenttest: Definition conformance %s case %d has an invalid name", c.kind, index)
	}
	if _, exists := c.names[name]; exists {
		return fmt.Errorf("agenttest: Definition conformance %s case name %q is duplicated", c.kind, name)
	}
	if c.names == nil {
		c.names = make(map[string]struct{})
	}
	c.names[name] = struct{}{}
	if !state.Valid() {
		return fmt.Errorf("agenttest: Definition conformance %s case %q has an invalid state", c.kind, name)
	}
	return nil
}

// verifyRejectedStep restores a fresh Execution per attempt because a failed
// Step discards its instance. Repeating the attempt proves the classification
// is a function of the captured state and input rather than of one instance.
func verifyRejectedStep(
	ctx context.Context,
	definition agent.Definition,
	sample RejectedStepConformanceCase,
) error {
	for attempt := range 2 {
		execution, err := callRestore(ctx, definition, sample.State)
		if err != nil {
			return err
		}
		transition, stepErr := callStep(ctx, execution, slices.Clone(sample.Signals))
		if stepErr == nil {
			return fmt.Errorf(
				"agenttest: rejected case %q attempt %d returned Transition %v instead of a classified Failure",
				sample.Name, attempt, transition,
			)
		}
		sealed, classified := errors.AsType[*agent.StepError](stepErr)
		if !classified {
			return fmt.Errorf(
				"agenttest: rejected case %q attempt %d returned an unclassified error, which the Engine records as execution.step.failed: %w",
				sample.Name, attempt, stepErr,
			)
		}
		if lo.IsNil(sealed) || !sealed.Failure.Valid() {
			return fmt.Errorf(
				"agenttest: rejected case %q attempt %d carries an invalid Failure",
				sample.Name, attempt,
			)
		}
		if sealed.Failure.Kind() != sample.FailureKind || sealed.Failure.Code() != sample.FailureCode {
			return fmt.Errorf(
				"agenttest: rejected case %q attempt %d classified as %s/%s, want %s/%s",
				sample.Name, attempt,
				sealed.Failure.Kind(), sealed.Failure.Code(),
				sample.FailureKind, sample.FailureCode,
			)
		}
	}
	return nil
}

func validateConformanceSignals(signals []agent.Signal, following ...[]agent.Signal) error {
	for batchIndex, batch := range append([][]agent.Signal{signals}, following...) {
		for index, signal := range batch {
			if !signal.Valid() {
				return fmt.Errorf("batch %d signal %d is invalid", batchIndex, index)
			}
		}
	}
	return nil
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

func verifyDescriptorStability(definition agent.Definition) error {
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
		return fmt.Errorf("agenttest: concurrent Descriptor results differ:\nfirst:  %s\nsecond: %s", encoded[0], encoded[1])
	}
	return nil
}

func verifyFreshExecutions(ctx context.Context, config DefinitionConformanceConfig) error {
	descriptorBefore, descriptorErr := callDescriptor(config.Definition)
	if descriptorErr != nil {
		return descriptorErr
	}
	if validationErr := descriptorBefore.ValidateInput(config.Input); validationErr != nil {
		return fmt.Errorf("agenttest: conformance Input does not satisfy Descriptor: %w", validationErr)
	}
	executions, err := callConcurrently(func() (agent.Execution, error) {
		return callStart(config.Definition, config.Input)
	})
	if err != nil {
		return err
	}
	if pairErr := verifyExecutionPair(ctx,
		config.Definition,
		executions[0],
		executions[1],
		config.InitialSignals, config.FollowingSignals...,
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
	definition agent.Definition,
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
	return verifyExecutionPair(ctx, definition, left, right, sample.Signals, sample.FollowingSignals...)
}

func verifyExecutionPair(ctx context.Context, definition agent.Definition, left, right agent.Execution, signals []agent.Signal, following ...[]agent.Signal) error {
	for _, batch := range append([][]agent.Signal{signals}, following...) {
		if err := verifyExecutionStep(ctx, definition, left, right, batch); err != nil {
			return err
		}
	}
	return nil
}

func verifyExecutionStep(
	ctx context.Context,
	definition agent.Definition,
	left agent.Execution,
	right agent.Execution,
	signals []agent.Signal,
) error {
	if lo.IsNil(left) || lo.IsNil(right) {
		return errors.New("agenttest: Definition returned a nil Execution")
	}
	leftBefore, rightBefore, err := snapshotEquivalentPair("initial Execution state", left, right)
	if err != nil {
		return err
	}
	if restoreErr := verifyExactRestore(ctx, definition, leftBefore); restoreErr != nil {
		return restoreErr
	}

	restored, err := callRestore(ctx, definition, leftBefore)
	if err != nil {
		return err
	}
	leftTransition, err := stepIsolatedPair(ctx, left, right, rightBefore, signals)
	if err != nil {
		return err
	}
	restoredTransition, err := callStep(ctx, restored, slices.Clone(signals))
	if err != nil {
		return err
	}
	if checkErr := requireEquivalent("original versus restored Step Transition", leftTransition, restoredTransition); checkErr != nil {
		return checkErr
	}

	leftAfter, _, err := snapshotEquivalentPair("original versus restored resulting state", left, restored)
	if err != nil {
		return err
	}
	rightAfter, err := callSnapshot(right)
	if err != nil {
		return err
	}
	if checkErr := requireEquivalent("resulting Execution state", leftAfter, rightAfter); checkErr != nil {
		return checkErr
	}
	return verifyExactRestore(ctx, definition, leftAfter)
}

// stepIsolatedPair steps left before right so a Step that mutates state shared
// with its sibling is visible in right's snapshot before right runs.
func stepIsolatedPair(
	ctx context.Context,
	left, right agent.Execution,
	rightBefore agent.ExecutionState,
	signals []agent.Signal,
) (agent.Transition, error) {
	leftTransition, err := callValidStep(ctx, left, signals)
	if err != nil {
		return agent.Transition{}, err
	}
	rightStill, err := callSnapshot(right)
	if err != nil {
		return agent.Transition{}, err
	}
	if comparisonErr := requireEquivalent("sibling Execution state after the first Step", rightBefore, rightStill); comparisonErr != nil {
		return agent.Transition{}, fmt.Errorf("%w: %w", errConformanceExecutionsShareState, comparisonErr)
	}
	rightTransition, err := callValidStep(ctx, right, signals)
	if err != nil {
		return agent.Transition{}, err
	}
	if err := requireEquivalent("Step Transition", leftTransition, rightTransition); err != nil {
		return agent.Transition{}, err
	}
	return leftTransition, nil
}

func snapshotEquivalentPair(label string, left, right agent.Execution) (agent.ExecutionState, agent.ExecutionState, error) {
	leftState, err := callSnapshot(left)
	if err != nil {
		return agent.ExecutionState{}, agent.ExecutionState{}, err
	}
	rightState, err := callSnapshot(right)
	if err != nil {
		return agent.ExecutionState{}, agent.ExecutionState{}, err
	}
	if err := requireEquivalent(label, leftState, rightState); err != nil {
		return agent.ExecutionState{}, agent.ExecutionState{}, err
	}
	return leftState, rightState, nil
}

func callValidStep(ctx context.Context, execution agent.Execution, signals []agent.Signal) (agent.Transition, error) {
	transition, err := callStep(ctx, execution, slices.Clone(signals))
	if err != nil {
		return agent.Transition{}, err
	}
	if err := validateTransition(transition, len(signals)); err != nil {
		return agent.Transition{}, err
	}
	return transition, nil
}

func verifyExactRestore(ctx context.Context, definition agent.Definition, state agent.ExecutionState) error {
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

func validateTransition(transition agent.Transition, signalCount int) error {
	if !transition.Valid() {
		return errors.New("agenttest: Step returned an invalid Transition")
	}
	if uint64(transition.ConsumedSignals()) > uint64(signalCount) {
		return fmt.Errorf(
			"agenttest: Step consumed %d Signals from a prefix of %d",
			transition.ConsumedSignals(), signalCount,
		)
	}
	return nil
}

func requireEquivalent(label string, left, right any) error {
	leftData, err := jsonv2.Marshal(left, jsonv2.Deterministic(true))
	if err != nil {
		return fmt.Errorf("agenttest: encode first %s: %w", label, err)
	}
	rightData, err := jsonv2.Marshal(right, jsonv2.Deterministic(true))
	if err != nil {
		return fmt.Errorf("agenttest: encode second %s: %w", label, err)
	}
	if !bytes.Equal(leftData, rightData) {
		return fmt.Errorf("%w: %s:\nfirst:  %s\nsecond: %s", errConformanceValuesDiffer, label, leftData, rightData)
	}
	return nil
}

func callDescriptor(definition agent.Definition) (descriptor agent.Descriptor, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &agent.CallbackPanicError{Operation: "Definition.Descriptor", Value: recovered}
		}
	}()
	descriptor = definition.Descriptor()
	if !descriptor.Valid() {
		return agent.Descriptor{}, errors.New("agenttest: Definition.Descriptor returned an invalid Descriptor")
	}
	return descriptor, nil
}

func callStart(definition agent.Definition, input agent.Payload) (execution agent.Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &agent.CallbackPanicError{Operation: "Definition.Start", Value: recovered}
		}
	}()
	execution, err = definition.Start(input)
	if err != nil {
		return nil, fmt.Errorf("agenttest: Definition.Start: %w", err)
	}
	if lo.IsNil(execution) {
		return nil, errors.New("agenttest: Definition.Start returned a nil Execution")
	}
	return execution, nil
}

func callRestore(ctx context.Context, definition agent.Definition, state agent.ExecutionState) (execution agent.Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &agent.CallbackPanicError{Operation: "Definition.Restore", Value: recovered}
		}
	}()
	execution, err = definition.Restore(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("agenttest: Definition.Restore: %w", err)
	}
	if lo.IsNil(execution) {
		return nil, errors.New("agenttest: Definition.Restore returned a nil Execution")
	}
	return execution, nil
}

func callStep(ctx context.Context, execution agent.Execution, signals []agent.Signal) (transition agent.Transition, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &agent.CallbackPanicError{Operation: "Execution.Step", Value: recovered}
		}
	}()
	transition, err = execution.Step(ctx, signals)
	if err != nil {
		return agent.Transition{}, fmt.Errorf("agenttest: Execution.Step: %w", err)
	}
	return transition, nil
}

func callSnapshot(execution agent.Execution) (state agent.ExecutionState, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &agent.CallbackPanicError{Operation: "Execution.Snapshot", Value: recovered}
		}
	}()
	state, err = execution.Snapshot()
	if err != nil {
		return agent.ExecutionState{}, fmt.Errorf("agenttest: Execution.Snapshot: %w", err)
	}
	if !state.Valid() {
		return agent.ExecutionState{}, errors.New("agenttest: Execution.Snapshot returned an invalid state")
	}
	return state, nil
}
