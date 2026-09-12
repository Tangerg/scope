package agenttest

import (
	"bytes"
	"context"
	"encoding/json"
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

// DefinitionConformanceConfig describes representative public boundary cases
// for one Definition. Conformance is evidence for these cases, not a proof that
// arbitrary implementation code never reads hidden input or performs I/O.
type DefinitionConformanceConfig struct {
	// Definition is the immutable behavior under test.
	Definition agent.Definition
	// Input is one valid value accepted by Definition.Start.
	Input agent.Input
	// InitialSignals are delivered to each fresh Execution created from Input.
	InitialSignals []agent.Signal
	// FollowingSignals exercises the original instance and each restored copy
	// through a representative multi-Step suffix.
	FollowingSignals [][]agent.Signal
	// RestoredCases exercise additional previously captured states.
	RestoredCases []ExecutionConformanceCase
}

// ExecutionConformanceCase describes one successful Restore and Step sample.
type ExecutionConformanceCase struct {
	// Name identifies the sample in test output.
	Name string
	// State is an exact state previously produced by the Definition.
	State agent.ExecutionState
	// Signals are the ordered Signal prefix delivered to Step.
	Signals          []agent.Signal
	FollowingSignals [][]agent.Signal
}

// RunDefinitionConformance verifies descriptor stability, concurrent Start
// isolation, exact Snapshot/Restore, and byte-equivalent Step results for the
// supplied representative cases. Step cases must describe successful Steps;
// Strategy-specific failure and cancellation paths remain ordinary tests owned
// by the Definition implementation.
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
		if err := verifyFreshExecutions(config); err != nil {
			t.Fatal(err)
		}
	})
	for _, sample := range config.RestoredCases {
		sample := sample
		t.Run("restored "+sample.Name, func(t *testing.T) {
			if err := verifyRestoredExecutions(config.Definition, sample); err != nil {
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
	if err := validateConformanceSignals(config.InitialSignals); err != nil {
		return fmt.Errorf("agenttest: Definition conformance initial Signals: %w", err)
	}
	names := make(map[string]struct{}, len(config.RestoredCases))
	for index, sample := range config.RestoredCases {
		if sample.Name == "" || strings.TrimSpace(sample.Name) != sample.Name {
			return fmt.Errorf("agenttest: Definition conformance restored case %d has an invalid name", index)
		}
		if _, exists := names[sample.Name]; exists {
			return fmt.Errorf("agenttest: Definition conformance restored case name %q is duplicated", sample.Name)
		}
		names[sample.Name] = struct{}{}
		if !sample.State.Valid() {
			return fmt.Errorf("agenttest: Definition conformance restored case %q has an invalid state", sample.Name)
		}
		if err := validateConformanceSignals(sample.Signals); err != nil {
			return fmt.Errorf("agenttest: Definition conformance restored case %q Signals: %w", sample.Name, err)
		}
	}
	return nil
}

func validateConformanceSignals(signals []agent.Signal) error {
	for index, signal := range signals {
		if !signal.Valid() {
			return fmt.Errorf("signal %d is invalid", index)
		}
	}
	return nil
}

func verifyDescriptorStability(definition agent.Definition) error {
	type result struct {
		data []byte
		err  error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			descriptor, err := callDescriptor(definition)
			if err != nil {
				results <- result{err: err}
				return
			}
			data, err := json.Marshal(descriptor)
			results <- result{data: data, err: err}
		}()
	}
	group.Wait()
	close(results)
	values := make([]result, 0, 2)
	for value := range results {
		if value.err != nil {
			return value.err
		}
		values = append(values, value)
	}
	if !bytes.Equal(values[0].data, values[1].data) {
		return fmt.Errorf("agenttest: concurrent Descriptor results differ:\nfirst:  %s\nsecond: %s", values[0].data, values[1].data)
	}
	return nil
}

func verifyFreshExecutions(config DefinitionConformanceConfig) error {
	descriptorBefore, descriptorErr := callDescriptor(config.Definition)
	if descriptorErr != nil {
		return descriptorErr
	}
	if validationErr := descriptorBefore.ValidateInput(config.Input); validationErr != nil {
		return fmt.Errorf("agenttest: conformance Input does not satisfy Descriptor: %w", validationErr)
	}
	type result struct {
		execution agent.Execution
		err       error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			execution, startErr := callStart(config.Definition, config.Input)
			results <- result{execution: execution, err: startErr}
		}()
	}
	group.Wait()
	close(results)
	values := make([]result, 0, 2)
	for value := range results {
		if value.err != nil {
			return value.err
		}
		values = append(values, value)
	}
	if pairErr := verifyExecutionPair(
		config.Definition,
		values[0].execution,
		values[1].execution,
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
	definition agent.Definition,
	sample ExecutionConformanceCase,
) error {
	left, err := callRestore(definition, sample.State)
	if err != nil {
		return err
	}
	right, err := callRestore(definition, sample.State)
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
	return verifyExecutionPair(definition, left, right, sample.Signals, sample.FollowingSignals...)
}

func verifyExecutionPair(definition agent.Definition, left, right agent.Execution, signals []agent.Signal, following ...[]agent.Signal) error {
	for _, batch := range append([][]agent.Signal{signals}, following...) {
		if err := validateConformanceSignals(batch); err != nil {
			return err
		}
		if err := verifyExecutionStep(definition, left, right, batch); err != nil {
			return err
		}
	}
	return nil
}

func verifyExecutionStep(
	definition agent.Definition,
	left agent.Execution,
	right agent.Execution,
	signals []agent.Signal,
) error {
	if lo.IsNil(left) || lo.IsNil(right) {
		return errors.New("agenttest: Definition returned a nil Execution")
	}
	leftBefore, err := callSnapshot(left)
	if err != nil {
		return err
	}
	rightBefore, err := callSnapshot(right)
	if err != nil {
		return err
	}
	if comparisonErr := requireEquivalent("initial Execution state", leftBefore, rightBefore); comparisonErr != nil {
		return comparisonErr
	}
	if restoreErr := verifyExactRestore(definition, leftBefore); restoreErr != nil {
		return restoreErr
	}

	restored, err := callRestore(definition, leftBefore)
	if err != nil {
		return err
	}
	leftTransition, err := callStep(left, slices.Clone(signals))
	if err != nil {
		return err
	}
	rightStill, err := callSnapshot(right)
	if err != nil {
		return err
	}
	if comparisonErr := requireEquivalent("sibling Execution state after the first Step", rightBefore, rightStill); comparisonErr != nil {
		return fmt.Errorf("%w: %w", errConformanceExecutionsShareState, comparisonErr)
	}
	rightTransition, err := callStep(right, slices.Clone(signals))
	if err != nil {
		return err
	}
	if validationErr := validateTransition(leftTransition, len(signals)); validationErr != nil {
		return validationErr
	}
	if validationErr := validateTransition(rightTransition, len(signals)); validationErr != nil {
		return validationErr
	}
	if comparisonErr := requireEquivalent("Step Transition", leftTransition, rightTransition); comparisonErr != nil {
		return comparisonErr
	}
	restoredTransition, err := callStep(restored, slices.Clone(signals))
	if err != nil {
		return err
	}
	if checkErr := requireEquivalent("original versus restored Step Transition", leftTransition, restoredTransition); checkErr != nil {
		return checkErr
	}
	restoredAfter, err := callSnapshot(restored)
	if err != nil {
		return err
	}

	leftAfter, err := callSnapshot(left)
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
	if checkErr := requireEquivalent("original versus restored resulting state", leftAfter, restoredAfter); checkErr != nil {
		return checkErr
	}
	return verifyExactRestore(definition, leftAfter)
}

func verifyExactRestore(definition agent.Definition, state agent.ExecutionState) error {
	restored, err := callRestore(definition, state)
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
	leftData, err := json.Marshal(left)
	if err != nil {
		return fmt.Errorf("agenttest: encode first %s: %w", label, err)
	}
	rightData, err := json.Marshal(right)
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
			err = fmt.Errorf("agenttest: Definition.Descriptor panicked: %v", recovered)
		}
	}()
	descriptor = definition.Descriptor()
	if !descriptor.Valid() {
		return agent.Descriptor{}, errors.New("agenttest: Definition.Descriptor returned an invalid Descriptor")
	}
	return descriptor, nil
}

func callStart(definition agent.Definition, input agent.Input) (execution agent.Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("agenttest: Definition.Start panicked: %v", recovered)
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

func callRestore(definition agent.Definition, state agent.ExecutionState) (execution agent.Execution, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("agenttest: Definition.Restore panicked: %v", recovered)
		}
	}()
	execution, err = definition.Restore(state)
	if err != nil {
		return nil, fmt.Errorf("agenttest: Definition.Restore: %w", err)
	}
	if lo.IsNil(execution) {
		return nil, errors.New("agenttest: Definition.Restore returned a nil Execution")
	}
	return execution, nil
}

func callStep(execution agent.Execution, signals []agent.Signal) (transition agent.Transition, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("agenttest: Execution.Step panicked: %v", recovered)
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
			err = fmt.Errorf("agenttest: Execution.Snapshot panicked: %v", recovered)
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
