package messaging_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/messaging"
)

func TestDispatcherRejectsNilContextBeforeProtocolValidation(t *testing.T) {
	dispatcher, err := messaging.NewDispatcher(messaging.DispatcherConfig{Port: &recipientPort{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("nil context became a protocol result")
		}
	}()
	var nilContext context.Context
	_, _ = dispatcher.Dispatch(nilContext, agent.EffectRequest{}, nil)
}

func TestMessageValidatesFrozenProtocol(t *testing.T) {
	var missing *recipientPort
	for _, port := range []messaging.DeliveryPort{nil, missing} {
		if dispatcher, err := messaging.NewDispatcher(messaging.DispatcherConfig{Port: port}); dispatcher != nil || !errors.Is(err, messaging.ErrNilDeliveryPort) {
			t.Fatalf("NewDispatcher with nil port = %v, %v", dispatcher, err)
		}
	}
	dispatcher, err := messaging.NewDispatcher(messaging.DispatcherConfig{Port: &recipientPort{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, dispatchErr := dispatcher.Dispatch(context.Background(), agent.EffectRequest{}, nil); !errors.Is(dispatchErr, messaging.ErrInvalidMessage) {
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
		effect, effectErr := agent.NewDispatcherEffect([]byte(payload))
		if effectErr != nil {
			t.Fatal(effectErr)
		}
		if policy := dispatcher.ReplayPolicy(effect); policy != agent.ReplayPolicyNever {
			t.Fatalf("invalid effect replay=%s payload=%s", policy, payload)
		}
	}
	if policy := dispatcher.ReplayPolicy(agent.Effect{}); policy != agent.ReplayPolicyNever {
		t.Fatalf("zero effect replay=%s", policy)
	}
	var absent *messaging.Dispatcher
	if policy := absent.ReplayPolicy(agent.Effect{}); policy != agent.ReplayPolicyNever {
		t.Fatalf("nil dispatcher replay=%s", policy)
	}
	if _, dispatchErr := absent.Dispatch(context.Background(), agent.EffectRequest{}, nil); !errors.Is(dispatchErr, messaging.ErrInvalidMessage) {
		t.Fatalf("nil dispatch=%v", dispatchErr)
	}
}

func TestSenderConformsAndMessageEffectOwnsRecipient(t *testing.T) {
	recipient, err := agent.ParseProcessID("process:original")
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := agent.ParseWaitID("wait:original")
	if err != nil {
		t.Fatal(err)
	}
	message := messaging.Message{Recipient: recipient, WaitID: &waitID, Payload: input(t, "review")}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{Definition: newSender(t), Input: input(t, message)})
	effect, err := message.Effect()
	if err != nil {
		t.Fatal(err)
	}
	waitID, err = agent.ParseWaitID("wait:replacement")
	if err != nil {
		t.Fatal(err)
	}
	frozenInput, err := agent.ParseInput(effect.Payload())
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
		engine, err := agent.NewEngine(agent.EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		receiver, err := engine.Start(t.Context(), bind(t, newGate(t), nil), input(t, "review"))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		waitID, _ := inspect(t, engine, receiver).WaitID()
		port := &recipientPort{engine: engine, recipient: receiver, firstAdmission: make(chan struct{}), release: make(chan struct{})}
		dispatcher, err := messaging.NewDispatcher(messaging.DispatcherConfig{Port: port})
		if err != nil {
			t.Fatal(err)
		}
		sender, err := engine.Start(t.Context(), bind(t, newSender(t), dispatcher), input(t, messaging.Message{Recipient: receiver.ID(), WaitID: &waitID, Payload: input(t, "review admitted")}))
		if err != nil {
			t.Fatal(err)
		}
		<-port.firstAdmission
		if cancelErr := sender.RequestCancellation(t.Context(), "cancel while confirmation is pending"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		result := finish(t, sender)
		if result.Status() != agent.StatusCanceled || len(result.Termination().UnresolvedEffectIDs()) != 1 || result.Usage().CommittedSteps != 0 {
			t.Fatalf("canceled send=%+v", result)
		}
		if result := finish(t, receiver); result.Status() != agent.StatusCompleted || result.Usage().AcceptedSignals != 2 {
			t.Fatalf("receiver=%+v", result)
		}
		closeEngine(t, engine)
	})
}
