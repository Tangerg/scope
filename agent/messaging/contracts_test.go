package messaging_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/messaging"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

func TestDispatcherRejectsNilContextBeforeProtocolValidation(t *testing.T) {
	dispatcher := newDispatcher(t, &recipientPort{})
	defer func() {
		if recover() == nil {
			t.Fatal("nil context became a protocol result")
		}
	}()
	var nilContext context.Context
	_, _ = dispatcher.Dispatch(nilContext, run.EffectRequest{}, nil)
}

func TestMessageValidatesFrozenProtocol(t *testing.T) {
	var missing *recipientPort
	for _, port := range []messaging.DeliveryPort{nil, missing} {
		if dispatcher, err := messaging.NewDispatcher(messaging.DispatcherConfig{Port: port}); dispatcher != nil || !errors.Is(err, messaging.ErrNilDeliveryPort) {
			t.Fatalf("NewDispatcher with nil port = %v, %v", dispatcher, err)
		}
	}
	dispatcher := newDispatcher(t, &recipientPort{})
	if _, dispatchErr := dispatcher.Dispatch(context.Background(), run.EffectRequest{}, nil); !errors.Is(dispatchErr, messaging.ErrInvalidMessage) {
		t.Fatalf("empty request=%v", dispatchErr)
	}
	if _, effectErr := (messaging.Message{}).Effect(); !errors.Is(effectErr, messaging.ErrInvalidMessage) {
		t.Fatalf("empty message=%v", effectErr)
	}
	for _, payload := range []string{
		`{"recipient":"process:peer","payload":"review","unknown":true}`,
		`{"recipient":"process:peer","payload":"review","wait_id":""}`,
		`{"payload":"review"}`, `null`,
	} {
		effect, effectErr := run.NewDispatcherEffect([]byte(payload))
		if effectErr != nil {
			t.Fatal(effectErr)
		}
		if policy := dispatcher.Policy(effect).Replay; policy != run.ReplayPolicyNever {
			t.Fatalf("invalid effect replay=%s payload=%s", policy, payload)
		}
	}
	if policy := dispatcher.Policy(run.Effect{}).Replay; policy != run.ReplayPolicyNever {
		t.Fatalf("zero effect replay=%s", policy)
	}
	var absent *messaging.Dispatcher
	if policy := absent.Policy(run.Effect{}).Replay; policy != run.ReplayPolicyNever {
		t.Fatalf("nil dispatcher replay=%s", policy)
	}
	if _, dispatchErr := absent.Dispatch(context.Background(), run.EffectRequest{}, nil); !errors.Is(dispatchErr, messaging.ErrInvalidMessage) {
		t.Fatalf("nil dispatch=%v", dispatchErr)
	}
}

func TestSenderConformsAndMessageEffectOwnsRecipient(t *testing.T) {
	recipient, err := run.ParseProcessID("process:original")
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := run.ParseWaitID("wait:original")
	if err != nil {
		t.Fatal(err)
	}
	message := messaging.Message{Recipient: recipient, WaitID: &waitID, Payload: input(t, "review")}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{Definition: newSender(t), Input: input(t, message)})
	effect, err := message.Effect()
	if err != nil {
		t.Fatal(err)
	}
	waitID, err = run.ParseWaitID("wait:replacement")
	if err != nil {
		t.Fatal(err)
	}
	frozenInput, err := run.ParsePayload(effect.Payload())
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := frozenInput.Decode[messaging.Message]()
	if err != nil {
		t.Fatal(err)
	}
	if frozen.WaitID == nil || frozen.WaitID.String() != "wait:original" {
		t.Fatal("prepared message changed its address")
	}
}

func TestCancellationCollectsUnacknowledgedDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine := newEngine(t, run.NewMemoryStore())
		receiver := start(t, engine, bind(t, newGate(t), nil), input(t, "review"))
		synctest.Wait()
		waitID, _ := inspect(t, engine, receiver).WaitID()
		port := &recipientPort{engine: engine, recipient: receiver, firstAdmission: make(chan struct{}), release: make(chan struct{})}
		sender := start(t, engine, bind(t, newSender(t), newDispatcher(t, port)), input(t, messaging.Message{Recipient: receiver.Relation().ProcessID(), WaitID: &waitID, Payload: input(t, "review admitted")}))
		<-port.firstAdmission
		if cancelErr := sender.Cancel(t.Context(), "cancel while confirmation is pending"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		result := finish(t, sender)
		if result.Termination().Status() != run.StatusCanceled || len(result.Termination().UnresolvedEffectIDs()) != 1 || result.Usage().CommittedSteps != 0 {
			t.Fatalf("canceled send=%+v", result)
		}
		if result := finish(t, receiver); result.Termination().Status() != run.StatusCompleted || result.Usage().AcceptedSignals != 2 {
			t.Fatalf("receiver=%+v", result)
		}
		closeEngine(t, engine)
	})
}

func TestMalformedMessageSettlesBeforeDelivery(t *testing.T) {
	dispatcher := newDispatcher(t, rejectingDeliveryPort{})
	for _, raw := range []string{`null`, `{}`, `{"recipient":"process:peer","payload":null,"unknown":true}`} {
		effect, effectErr := run.NewDispatcherEffect([]byte(raw))
		if effectErr != nil {
			t.Fatal(effectErr)
		}
		conformancetest.CheckDispatcherRejection(t, dispatcher, effect)
	}
	effect, err := run.NewDispatcherEffect([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	conformancetest.CheckDispatcherRejection(t, &messaging.Dispatcher{}, effect)
}

type rejectingDeliveryPort struct{}

func (rejectingDeliveryPort) Deliver(context.Context, run.ProcessID, run.ProcessID, run.SignalRequest) error {
	panic("malformed message reached delivery")
}
