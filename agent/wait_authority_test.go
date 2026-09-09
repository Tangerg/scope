package agent

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type multipleWaitDefinition struct{ *engineTestDefinition }

func (m *multipleWaitDefinition) Start(input Input) (Execution, error) {
	execution, err := m.engineTestDefinition.Start(input)
	if err != nil {
		return nil, err
	}
	return &multipleWaitExecution{execution.(*engineTestExecution)}, nil
}

func (m *multipleWaitDefinition) Restore(state ExecutionState) (Execution, error) {
	execution, err := m.engineTestDefinition.Restore(state)
	if err != nil {
		return nil, err
	}
	return &multipleWaitExecution{execution.(*engineTestExecution)}, nil
}

type multipleWaitExecution struct{ *engineTestExecution }

func (m *multipleWaitExecution) Step(ctx context.Context, signals []Signal) (Transition, error) {
	phase := m.state.Phase
	transition, err := m.engineTestExecution.Step(ctx, signals)
	if err != nil {
		return Transition{}, err
	}
	switch phase {
	case "ready":
		key, _ := ParseWaitKey("secondary")
		effect, err := RequestWait(key, []byte(`{"kind":"wait_opened"}`))
		if err != nil {
			return Transition{}, err
		}
		return Continue(0, append(transition.Effects(), effect)...)
	case "wait_id":
		waitID, _ := transition.WaitID()
		return Wait(uint32(len(signals)), waitID)
	default:
		return transition, nil
	}
}

func TestWaitingSignalBatchMustFirstAddressCurrentWait(t *testing.T) {
	for _, includeCurrent := range []bool{false, true} {
		name := "other_wait_only"
		if includeCurrent {
			name = "other_wait_before_current"
		}
		t.Run(name, func(t *testing.T) {
			engine, err := NewEngine(EngineConfig{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			})
			definition := &multipleWaitDefinition{newEngineTestDefinition(t, "engine.wait", "wait")}
			deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
			input, _ := EncodeInput(engineTestInput{Value: "waiting"})
			process, err := engine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if killErr := process.Kill(context.Background(), "test complete"); killErr != nil && !errors.Is(killErr, ErrProcessFinished) {
					t.Error(killErr)
				}
				awaitResult(t, process)
			})
			waitForStatus(t, process, StatusWaiting)
			before := inspectProcessSnapshot(t, process)
			wire, err := before.wire()
			if err != nil {
				t.Fatal(err)
			}
			current, _ := before.WaitID()
			var other WaitID
			for _, wait := range wire.Mailbox.Waits {
				if wait.WaitID != current {
					other = wait.WaitID
				}
			}
			if !other.Valid() {
				t.Fatal("second wait was not opened")
			}
			currentID, _ := ParseSignalID("signal:current")
			otherID, _ := ParseSignalID("signal:other")
			answer, _ := NewSignalRequest(currentID, current, []byte(`{"kind":"answer","value":"approved"}`))
			otherAnswer, _ := NewSignalRequest(otherID, other, []byte(`{"kind":"answer","value":"secondary"}`))
			requests := []SignalRequest{otherAnswer}
			if includeCurrent {
				requests = append(requests, answer)
			}
			usage := inspectProcessSnapshot(t, process).Usage()
			if accepted, deliveryErr := process.DeliverSignals(t.Context(), requests...); accepted || !errors.Is(deliveryErr, ErrSignalRejected) {
				t.Fatalf("non-current wait batch = %t, %v; want false, ErrSignalRejected", accepted, deliveryErr)
			}
			after := inspectProcessSnapshot(t, process)
			if !bytes.Equal(before.JSON(), after.JSON()) || inspectProcessSnapshot(t, process).Usage() != usage {
				t.Fatal("rejected batch changed the snapshot or usage")
			}
			if accepted, deliveryErr := process.DeliverSignals(t.Context(), answer, otherAnswer); !accepted || deliveryErr != nil {
				t.Fatalf("current wait first = %t, %v", accepted, deliveryErr)
			}
			result := awaitResult(t, process)
			output, _ := result.Output()
			value, err := output.Decode[engineTestOutput]()
			if result.Status() != StatusCompleted || err != nil || value.Value != "approved" || inspectProcessSnapshot(t, process).Usage().AcceptedSignals != 4 {
				t.Fatalf("result = %s, %+v, %v; usage = %+v", result.Status(), value, err, inspectProcessSnapshot(t, process).Usage())
			}
		})
	}
}
