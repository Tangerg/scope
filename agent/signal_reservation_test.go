package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

type fixtureReservationDefinition struct {
	base              *childTestDefinition
	consumeWithEffect bool
}

func (f *fixtureReservationDefinition) Descriptor() Descriptor { return f.base.Descriptor() }
func (f *fixtureReservationDefinition) Start(input Input) (Execution, error) {
	value, err := f.base.Start(input)
	if err != nil {
		return nil, err
	}
	return &fixtureReservationExecution{base: value.(*childTestExecution), consumeWithEffect: f.consumeWithEffect}, nil
}
func (f *fixtureReservationDefinition) Restore(state ExecutionState) (Execution, error) {
	value, err := f.base.Restore(state)
	if err != nil {
		return nil, err
	}
	return &fixtureReservationExecution{base: value.(*childTestExecution), consumeWithEffect: f.consumeWithEffect}, nil
}

type fixtureReservationExecution struct {
	base              *childTestExecution
	consumeWithEffect bool
}

func (f *fixtureReservationExecution) Snapshot() (ExecutionState, error) { return f.base.Snapshot() }
func (f *fixtureReservationExecution) Step(ctx context.Context, signals []Signal) (Transition, error) {
	if f.base.state.Mode != "nested_wait" {
		return f.base.Step(ctx, signals)
	}
	switch f.base.state.Phase {
	case "wait_opened":
		if _, err := f.base.Step(ctx, signals); err != nil {
			return Transition{}, err
		}
		if f.consumeWithEffect {
			return f.startEffect(uint32(len(signals)))
		}
		f.base.state.Phase = "start_parent_effect"
		return Continue(uint32(len(signals)))
	case "start_parent_effect":
		return f.startEffect(0)
	case "parent_effect":
		output, err := EncodeOutput(childTestOutput{})
		if err != nil {
			return Transition{}, err
		}
		return Complete(uint32(len(signals)), output)
	default:
		return f.base.Step(ctx, signals)
	}
}

func (f *fixtureReservationExecution) startEffect(consumed uint32) (Transition, error) {
	f.base.state.Phase = "parent_effect"
	effect, err := NewDispatcherEffect([]byte(`{"value":"parent"}`))
	if err != nil {
		return Transition{}, err
	}
	return Continue(consumed, effect)
}

func TestChildCompletionPreservesSettlementCapacity(t *testing.T) {
	for _, test := range []struct {
		name              string
		limit             uint64
		consumeWithEffect bool
		wantStatus        Status
		wantAccepted      uint64
	}{
		{name: "reserved mailbox", limit: 1, wantStatus: StatusFailed},
		{name: "available mailbox", limit: 2, wantStatus: StatusCompleted, wantAccepted: 1},
		{name: "prepared consumption", limit: 2, consumeWithEffect: true, wantStatus: StatusCompleted, wantAccepted: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			release := make(chan struct{})
			dispatcher := &engineTestDispatcher{
				policy: ReplayPolicySameIdentity, started: make(chan struct{}, 1), block: release,
			}
			base := newChildTestDeployment(t)
			definition := &fixtureReservationDefinition{
				base: base.Definition().(*childTestDefinition), consumeWithEffect: test.consumeWithEffect,
			}
			deployment, err := NewDeployment(DeploymentConfig{
				Definition: definition, Dispatcher: dispatcher,
				ImplementationDigest: ComputeDigest([]byte("reservation-implementation")),
				ConfigurationDigest:  ComputeDigest([]byte("reservation-configuration")),
			})
			if err != nil {
				t.Fatal(err)
			}
			definition.base.reference = deployment.DeploymentRef()
			limits := DefaultLimits()
			limits.MaxPendingSignals = test.limit
			engine, err := NewEngine(EngineConfig{Limits: limits})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			})
			releaseEffect := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseEffect)
			input, err := EncodeInput(childTestInput{Mode: "nested_wait"})
			if err != nil {
				t.Fatal(err)
			}
			root, err := engine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				releaseEffect()
				if _, awaitErr := root.Await(context.WithoutCancel(t.Context())); awaitErr != nil {
					t.Error(awaitErr)
				}
			})
			<-dispatcher.started
			before := root.Usage()
			ids := directChildIDs(t, engine, root.ID())
			if len(ids) != 1 {
				t.Fatalf("children=%v", ids)
			}
			childID, _ := ParseProcessID(ids[0])
			child, found := engine.Process(childID)
			if !found {
				t.Fatal("child missing")
			}
			waitForProcessStatus(t, child, StatusWaiting)
			waitID, _ := child.WaitID()
			signalID, _ := ParseSignalID("signal:complete-reserved-child")
			request, err := NewSignalRequest(signalID, waitID, []byte(`{"reply":"done"}`))
			if err != nil {
				t.Fatal(err)
			}
			if accepted, deliveryErr := child.DeliverSignals(t.Context(), request); deliveryErr != nil || !accepted {
				t.Fatalf("child delivery=%t error=%v", accepted, deliveryErr)
			}
			if result, awaitErr := child.Await(t.Context()); awaitErr != nil || result.Status() != StatusCompleted {
				t.Fatalf("child status=%s error=%v", result.Status(), awaitErr)
			}
			if _, snapshotErr := root.Snapshot(t.Context()); snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			if accepted := root.Usage().AcceptedSignals - before.AcceptedSignals; accepted != test.wantAccepted {
				t.Fatalf("accepted child signals=%d, want %d", accepted, test.wantAccepted)
			}
			releaseEffect()
			result, err := root.Await(t.Context())
			if err != nil || result.Status() != test.wantStatus {
				t.Fatalf("root status=%s, want %s, error=%v", result.Status(), test.wantStatus, err)
			}
			if test.wantStatus == StatusFailed {
				failure, present := result.Termination().Failure()
				if !present || failure.Code() != "engine.limit.child_completion_signal" {
					t.Fatalf("failure=%+v, present=%t", failure, present)
				}
			}
		})
	}
}

func TestSnapshotRejectsUnfundedSignalReservations(t *testing.T) {
	for _, test := range []struct {
		name   string
		modify func(*processSnapshotWire)
	}{
		{name: "pending mailbox", modify: func(wire *processSnapshotWire) { wire.Limits.MaxPendingSignals = 1 }},
		{name: "lifetime signals", modify: func(wire *processSnapshotWire) {
			wire.Limits.MaxSignals = 1
			wire.Limits.MaxPendingSignals = 1
			wire.Budget.Signals = 1
		}},
		{name: "child allocation", modify: func(wire *processSnapshotWire) {
			wire.ReservedBudget.Signals = wire.Budget.Signals - 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := preparedEngineTestSnapshot(t)
			wire, err := snapshot.wire()
			if err != nil {
				t.Fatal(err)
			}
			mailbox, err := restoreSignalMailbox(wire.Mailbox)
			if err != nil {
				t.Fatal(err)
			}
			signalID, _ := ParseSignalID("signal:unfunded-reservation")
			signal, err := newSignal(signalID, WaitID{}, []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if accepted, enqueueErr := mailbox.enqueue(StatusRunning, signal, signalSourceExternal); enqueueErr != nil || !accepted {
				t.Fatalf("enqueue=%t error=%v", accepted, enqueueErr)
			}
			wire.Mailbox = mailbox.snapshot()
			wire.Usage.AcceptedSignals++
			test.modify(&wire)
			data, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if _, parseErr := ParseProcessSnapshot(data); !errors.Is(parseErr, ErrInvalidSnapshot) {
				t.Fatalf("unfunded reservation error=%v", parseErr)
			}
		})
	}
}
