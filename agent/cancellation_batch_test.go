package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"testing/synctest"
)

func TestInterruptedBatchRetainsItsSettledPrefixAndUnstartedStructuralEffects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		childDeployment := newChildTestDeployment(t)
		childKey, _ := ParseChildKey("unstarted")
		childInput, _ := EncodeInput(childTestInput{Mode: "leaf"})
		child, err := StartChild(childTestSpec(childKey, childDeployment.DeploymentRef(), childInput))
		if err != nil {
			t.Fatal(err)
		}
		waitKey, _ := ParseWaitKey("unopened")
		wait, err := RequestWait(waitKey, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		external, _ := NewDispatcherEffect(json.RawMessage(`{}`))
		definition := &effectSequenceDefinition{
			descriptor: newEngineTestDefinition(t, "engine.sequence", "effect").Descriptor(),
			effects:    []Effect{external, external, external, child, wait},
		}
		dispatcher := &cancellationDispatcher{
			entered: make(chan EffectRequest, 5), canceled: make(chan struct{}),
			release: make(chan struct{}),
			status:  SettlementStatusSucceeded, frontier: 1,
		}
		release := sync.OnceFunc(func() { close(dispatcher.release) })
		defer release()
		deployment := engineTestDeployment(t, definition, dispatcher)
		engine, err := NewEngine(EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		input, _ := EncodeInput(engineTestInput{Value: "batch"})
		process, err := engine.Start(t.Context(), deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		prefix, frontier := <-dispatcher.entered, <-dispatcher.entered
		if prefix.BatchIndex() != 0 || frontier.BatchIndex() != 1 {
			t.Fatal("effects did not enter in declaration order")
		}
		if cancelErr := process.RequestCancellation(t.Context(), "stop the remaining batch"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		<-dispatcher.canceled
		release()
		result := mustAwait(t, process)
		if result.Status() != StatusCanceled || result.Termination().Cause() != TerminationCauseHostCancellation {
			t.Fatalf("termination = %+v", result.Termination())
		}
		wire, err := inspectProcessSnapshot(t, process).wire()
		if err != nil {
			t.Fatal(err)
		}
		if wire.Prepared == nil || len(wire.Prepared.Effects) != len(definition.effects) {
			t.Fatalf("interrupted batch = %+v", wire.Prepared)
		}
		for index, effect := range wire.Prepared.Effects {
			if index < 2 {
				if !effect.definitelySettled() || string(effect.Settlement.Payload()) != `{"done":true}` {
					t.Errorf("settled prefix[%d] = %+v", index, effect)
				}
			} else if effect.Phase != effectPhasePlanned || effect.Settlement != nil || effect.WaitID != nil {
				t.Errorf("unstarted tail[%d] = %+v", index, effect)
			}
		}
		if wire.ReservedBudget != (Budget{}) || len(directChildIDs(t, engine, process.ID())) != 0 || len(wire.Mailbox.Waits) != 0 {
			t.Errorf("unstarted structural effects acquired resources: budget=%+v mailbox=%+v", wire.ReservedBudget, wire.Mailbox)
		}
		if len(dispatcher.entered) != 0 {
			t.Error("a later external Effect started")
		}
		mustCloseEngine(t, engine)
	})
}

func TestCancellationPreservesPreparedInputInAFullMailbox(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := newChildTestDeployment(t)
		definition := &fixtureReservationDefinition{
			base: base.Definition().(*childTestDefinition), consumeWithEffect: true,
		}
		dispatcher := &cancellationDispatcher{
			entered: make(chan EffectRequest, 1), canceled: make(chan struct{}),
			release: make(chan struct{}), status: SettlementStatusSucceeded,
		}
		release := sync.OnceFunc(func() { close(dispatcher.release) })
		defer release()
		deployment, err := NewDeployment(DeploymentConfig{
			Definition: definition, Dispatcher: dispatcher,
			ImplementationDigest: ComputeDigest([]byte("interrupted-input")),
			ConfigurationDigest:  ComputeDigest([]byte("full-mailbox")),
		})
		if err != nil {
			t.Fatal(err)
		}
		definition.base.reference = deployment.DeploymentRef()
		limits := DefaultLimits()
		limits.MaxPendingSignals = 1
		engine, err := NewEngine(EngineConfig{Limits: limits})
		if err != nil {
			t.Fatal(err)
		}
		input, _ := EncodeInput(childTestInput{Mode: "nested_wait"})
		process, err := engine.Start(t.Context(), deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		<-dispatcher.entered
		before, err := inspectProcessSnapshot(t, process).wire()
		if err != nil {
			t.Fatal(err)
		}
		if before.Prepared == nil || before.Prepared.Transition.ConsumedSignals() != 1 || len(before.Mailbox.Signals) == 0 {
			t.Fatalf("fixture did not prepare input consumption: %+v", before)
		}
		if killErr := process.Kill(t.Context(), "retain input at cancellation"); killErr != nil {
			t.Fatal(killErr)
		}
		<-dispatcher.canceled
		release()
		result := mustAwait(t, process)
		if result.Status() != StatusKilled {
			t.Fatalf("termination = %+v", result.Termination())
		}
		after, err := inspectProcessSnapshot(t, process).wire()
		if err != nil {
			t.Fatal(err)
		}
		beforeSignals, _ := json.Marshal(before.Mailbox.Signals)
		afterSignals, _ := json.Marshal(after.Mailbox.Signals)
		if !bytes.Equal(beforeSignals, afterSignals) || before.Mailbox.SignalCursor != after.Mailbox.SignalCursor ||
			before.Usage != after.Usage || !bytes.Equal(before.CommittedExecutionState.Payload(), after.CommittedExecutionState.Payload()) {
			t.Errorf("cancellation changed committed input or state: before=%+v after=%+v", before, after)
		}
		if after.Prepared == nil || !after.Prepared.Effects[0].definitelySettled() {
			t.Errorf("settlement evidence = %+v", after.Prepared)
		}
		awaitChildren(t, engine, directChildIDs(t, engine, process.ID()))
		mustCloseEngine(t, engine)
	})
}

type effectSequenceDefinition struct {
	descriptor Descriptor
	effects    []Effect
}

func (e *effectSequenceDefinition) Descriptor() Descriptor { return e.descriptor }

func (e *effectSequenceDefinition) Start(input Input) (Execution, error) {
	value, err := input.Decode[engineTestInput]()
	if err != nil {
		return nil, err
	}
	return &effectSequenceExecution{definition: e, state: engineTestState{Phase: "ready", Value: value.Value}}, nil
}

func (e *effectSequenceDefinition) Restore(state ExecutionState) (Execution, error) {
	if state.Kind() != e.descriptor.Name() {
		return nil, ErrInvalidExecutionState
	}
	value, err := wireJSON.decode[engineTestState](state.Payload())
	if err != nil {
		return nil, err
	}
	return &effectSequenceExecution{definition: e, state: value}, nil
}

type effectSequenceExecution struct {
	definition *effectSequenceDefinition
	state      engineTestState
}

func (e *effectSequenceExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	if e.state.Phase == "ready" {
		e.state.Phase = "settled"
		return Continue(uint32(len(signals)), e.definition.effects...)
	}
	output, err := EncodeOutput(engineTestOutput{Value: e.state.Value})
	if err != nil {
		return Transition{}, err
	}
	return Complete(uint32(len(signals)), output)
}

func (e *effectSequenceExecution) Snapshot() (ExecutionState, error) {
	payload, err := json.Marshal(e.state)
	if err != nil {
		return ExecutionState{}, err
	}
	return NewExecutionState(e.definition.descriptor.Name(), payload)
}
