package messaging

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
)

// ErrNilDeliveryPort rejects construction without a delivery authority.
var ErrNilDeliveryPort = errors.New("messaging: delivery port is required")

// DeliveryPort admits one Signal to the supplied concrete recipient. The
// implementation owns destination authority and payload validation, preserves
// the recipient across replay, and uses atomic single-Signal admission. A
// nil error confirms that this exact Signal was admitted, either by this call
// or an earlier identical delivery. New and duplicate admissions share the
// same acknowledgment contract; the mailbox owns admission accounting. Errors
// never prove the message was not admitted by this attempt or a previous one. Calls must
// honor ctx, be bounded and concurrency-safe, and may reconcile authoritative
// receipts before reporting a terminal recipient error. Retention must preserve
// identity conflicts and admission evidence for the entire replay obligation.
type DeliveryPort interface {
	Deliver(ctx context.Context, sender, recipient agent.ProcessID, signal agent.SignalRequest) error
}

// DispatcherConfig binds the only delivery authority used by this Dispatcher.
// Its policy and routing configuration belong in the Deployment's exact binding.
type DispatcherConfig struct {
	Port DeliveryPort
}

// Dispatcher delivers each frozen Message under one Effect-derived SignalID.
// It owns no retry loop, mailbox, routing registry, or background work.
type Dispatcher struct {
	port DeliveryPort
}

func NewDispatcher(config DispatcherConfig) (*Dispatcher, error) {
	if lo.IsNil(config.Port) {
		return nil, ErrNilDeliveryPort
	}
	return &Dispatcher{port: config.Port}, nil
}

// Receipt is the successful admission result carried by a settlement Signal.
// It establishes delivery, not the receiver's committed consumption.
type Receipt struct {
	Recipient agent.ProcessID `json:"recipient"`
	SignalID  agent.SignalID  `json:"signal_id"`
}

func (d *Dispatcher) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	if d == nil || lo.IsNil(d.port) {
		return agent.ReplayPolicyNever
	}
	if _, err := decodeMessage(effect); err != nil {
		return agent.ReplayPolicyNever
	}
	return agent.ReplayPolicySameIdentity
}

// Dispatch requires a non-nil context and panics if ctx is nil.
func (d *Dispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, _ agent.DeltaEmitter) (agent.Settlement, error) {
	if ctx == nil {
		panic(errors.New("messaging: nil Context"))
	}
	if d == nil || lo.IsNil(d.port) || !request.Valid() {
		return agent.Settlement{}, ErrInvalidMessage
	}
	message, err := decodeMessage(request.Effect())
	if err != nil {
		return agent.Settlement{}, err
	}
	id, err := agent.ParseSignalID("signal:message:" + agent.ComputeDigest([]byte(request.ID().String())).String())
	if err != nil {
		return agent.Settlement{}, err
	}
	var waitID agent.WaitID
	if message.WaitID != nil {
		waitID = *message.WaitID
	}
	signal, err := agent.NewSignalRequest(id, waitID, message.Payload.JSON())
	if err != nil {
		return agent.Settlement{}, err
	}
	if deliveryErr := d.port.Deliver(ctx, request.ProcessID(), message.Recipient, signal); deliveryErr != nil {
		return agent.Settlement{}, deliveryErr
	}
	payload, err := json.Marshal(Receipt{Recipient: message.Recipient, SignalID: id})
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(request.ID(), agent.SettlementStatusSucceeded, payload)
}
