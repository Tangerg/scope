package messaging

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
)

type uncalledPort struct{}

func (*uncalledPort) Deliver(context.Context, run.ProcessID, run.ProcessID, run.Signal) error {
	panic("invalid protocol reached delivery")
}

func TestDeliveryAuthorityIsRequiredBeforeDispatch(t *testing.T) {
	var typedNil *uncalledPort
	for _, port := range []DeliveryPort{nil, typedNil} {
		if dispatcher, err := NewDispatcher(DispatcherConfig{Port: port}); dispatcher != nil || !errors.Is(err, ErrNilDeliveryPort) {
			t.Fatalf("missing delivery authority produced %v, %v", dispatcher, err)
		}
	}
}

func TestMalformedMessageIsDefinitelyRejectedWithoutDeliveryOrReplay(t *testing.T) {
	dispatcher := require(NewDispatcher(DispatcherConfig{Port: &uncalledPort{}}))
	for _, raw := range []string{
		`null`, `{}`, `{"recipient":"peer","payload":"answer","unexpected":true}`,
		`{"recipient":"peer","payload":"answer","wait_key":""}`,
		`{"payload":"answer"}`, `{"recipient":"peer"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			effect := require(run.NewDispatcherEffect(require(run.ParsePayload([]byte(raw)))))
			if policy := dispatcher.Policy(effect); policy.Replay != run.ReplayPolicyNever {
				t.Fatalf("invalid message authorized replay: %+v", policy)
			}
			conformancetest.CheckDispatcherRejection(t, dispatcher, effect)
		})
	}
	for _, absent := range []*Dispatcher{nil, {}} {
		if policy := absent.Policy(run.Effect{}); policy.Replay != run.ReplayPolicyNever {
			t.Fatalf("missing authority authorized replay: %+v", policy)
		}
	}
	if policy := dispatcher.Policy(run.Effect{}); policy.Replay != run.ReplayPolicyNever {
		t.Fatalf("empty effect authorized replay: %+v", policy)
	}
	message := Message{Recipient: require(run.ParseProcessID("peer")), Payload: require(run.EncodePayload("answer"))}
	conformancetest.CheckDispatcherRejection(t, &Dispatcher{}, require(message.Effect()))
}

type capturedDelivery struct {
	sender, recipient run.ProcessID
	signal            run.Signal
}

type capturePort struct{ deliveries chan capturedDelivery }

func (c capturePort) Deliver(_ context.Context, sender, recipient run.ProcessID, signal run.Signal) error {
	c.deliveries <- capturedDelivery{sender: sender, recipient: recipient, signal: signal}
	return nil
}

func TestUnaddressedMessageRetainsNullPayloadAndEffectDerivedReceipt(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	port := capturePort{deliveries: make(chan capturedDelivery, 2)}
	program := senderProgram(t, runtime, port)
	message := Message{Recipient: require(run.ParseProcessID("peer")), Payload: require(run.EncodePayload[any](nil))}
	if !message.Valid() {
		t.Fatal("present null payload was treated as missing")
	}
	var previous run.SignalID
	for _, key := range []string{"first", "second"} {
		handle := require(runtime.Start(t.Context(), program, run.StartRequest[Message]{ID: run.StartID{Scope: "message-boundary", Key: key}, Input: message}))
		result := require(handle.Await(t.Context()))
		receipt := require(result.Output())
		delivery := <-port.deliveries
		if delivery.sender != handle.ID() || delivery.recipient != message.Recipient || delivery.signal.Wait.Valid() || string(delivery.signal.Payload.JSON()) != "null" {
			t.Fatalf("delivery changed the frozen address or payload: %+v", delivery)
		}
		if receipt.SignalID != delivery.signal.ID || receipt.SignalID == previous {
			t.Fatal("receipt did not identify one distinct sending effect")
		}
		previous = receipt.SignalID
	}
}
