package messaging

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/strategy/stepfail"
	"github.com/Tangerg/scope/agent/run"
)

// ErrNilDeliveryPort rejects construction without a delivery authority.
var ErrNilDeliveryPort = errors.New("messaging: delivery port is required")

// DeliveryPort admits one Signal to the supplied concrete recipient. The
// implementation owns destination authority and payload validation, preserves
// the recipient across replay, and uses atomic single-Signal admission. A nil
// error confirms that this exact Signal was admitted, either by this call or an
// earlier identical delivery. New and duplicate admissions share the same
// acknowledgment contract; the mailbox owns admission accounting. Errors never
// prove the message was not admitted by this attempt or a previous one. Calls
// must honor ctx, be bounded and concurrency-safe, and may reconcile
// authoritative receipts before reporting a terminal recipient error. Retention
// must preserve identity conflicts and admission evidence for the entire replay
// obligation.
type DeliveryPort interface {
	Deliver(ctx context.Context, sender, recipient run.ProcessID, signal run.Signal) error
}

// DispatcherConfig binds the only delivery authority used by this Dispatcher.
// Its policy and routing configuration belong in the Program's exact binding.
type DispatcherConfig struct {
	Port DeliveryPort
}

// Dispatcher delivers each frozen Message under one Effect-derived SignalID.
// It owns no retry loop, mailbox, routing registry, or background work.
// For valid Runtime requests, local rejection settles Failed with a typed
// diagnostic. Success carries Receipt; every DeliveryPort error remains
// unknown because it cannot disprove an earlier admission.
type Dispatcher struct {
	port DeliveryPort
}

func NewDispatcher(config DispatcherConfig) (*Dispatcher, error) {
	if lo.IsNil(config.Port) {
		return nil, ErrNilDeliveryPort
	}
	return &Dispatcher{port: config.Port}, nil
}

// Receipt is the successful admission result carried by a Completion.
// It establishes delivery, not the receiver's committed consumption. The
// Message owns its recipient; the receipt carries only the SignalID the
// recipient admitted, which derives from the sending Effect.
type Receipt struct {
	SignalID run.SignalID `json:"signal_id"`
}

func (d *Dispatcher) Policy(effect run.Effect) run.EffectPolicy {
	if d == nil || lo.IsNil(d.port) {
		return run.EffectPolicy{Replay: run.ReplayPolicyNever}
	}
	if _, err := decodeMessage(effect); err != nil {
		return run.EffectPolicy{Replay: run.ReplayPolicyNever}
	}
	return run.EffectPolicy{Replay: run.ReplayPolicySameIdentity}
}

// Dispatch requires a non-nil context and panics if ctx is nil.
func (d *Dispatcher) Dispatch(ctx context.Context, request run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	ctx = run.RequireContext(ctx)
	if !request.Valid() {
		return run.Settlement{}, ErrInvalidMessage
	}
	if d == nil || lo.IsNil(d.port) {
		return messageFailureSettlement(ErrInvalidMessage)
	}
	message, err := decodeMessage(request.Effect())
	if err != nil {
		return messageFailureSettlement(err)
	}
	signal, err := message.signal(request.ID())
	if err != nil {
		return messageFailureSettlement(err)
	}
	if deliveryErr := d.port.Deliver(ctx, request.ID().Process, message.Recipient, signal); deliveryErr != nil {
		return run.Settlement{}, deliveryErr
	}
	payload, err := jsonv2.Marshal(Receipt{SignalID: signal.ID})
	if err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload)
}

var _ run.Dispatcher = (*Dispatcher)(nil)

const failureCodeDispatcher = "messaging.dispatch.rejected"

func messageFailureSettlement(cause error) (run.Settlement, error) {
	return stepfail.Settlement(run.FailureKindContract, failureCodeDispatcher, cause.Error())
}
