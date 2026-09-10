package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const nullJSON = "null"

var errInvalidReplayPolicy = errors.New("agent: invalid Dispatcher replay policy")

func (p *preparedEffectWire) settleFramework() error {
	var header struct {
		Operation frameworkEffectOperation `json:"operation"`
	}
	if err := json.Unmarshal(p.Effect.Payload(), &header); err != nil {
		return p.settleUnknown()
	}
	switch header.Operation {
	case frameworkEffectWait:
		_, payload, err := decodeWaitRequest(p.Effect)
		if err != nil {
			return p.settleUnknown()
		}
		waitID := deriveWaitID(p.ID)
		settlement, err := NewSettlement(p.ID, SettlementStatusSucceeded, payload)
		if err != nil {
			return p.settleUnknown()
		}
		p.WaitID = &waitID
		return p.settle(settlement)
	case frameworkEffectStartChild:
		// Child start crosses admission and initialization boundaries. treeRuntime
		// intercepts it and commits its fenced job completion atomically.
		return p.settleUnknown()
	case frameworkEffectWaitChildren:
		spec, err := decodeChildWaitEffect(p.Effect.Payload())
		if err != nil {
			return p.settleUnknown()
		}
		payload, err := encodeChildWaitOpened(spec)
		if err != nil {
			return p.settleUnknown()
		}
		waitID := deriveWaitID(p.ID)
		settlement, err := NewSettlement(p.ID, SettlementStatusSucceeded, payload)
		if err != nil {
			return p.settleUnknown()
		}
		p.WaitID = &waitID
		return p.settle(settlement)
	default:
		return p.settleUnknown()
	}
}

func dispatcherReplayPolicy(
	dispatcher Dispatcher,
	effect Effect,
) (policy ReplayPolicy, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			policy = ReplayPolicyInvalid
			err = fmt.Errorf("%w: panic: %v", errInvalidReplayPolicy, recovered)
		}
	}()
	policy = dispatcher.ReplayPolicy(effect)
	if !policy.Valid() {
		return ReplayPolicyInvalid, errInvalidReplayPolicy
	}
	return policy, nil
}

func dispatchEffect(
	ctx context.Context,
	dispatcher Dispatcher,
	request EffectRequest,
	emit DeltaEmitter,
) (settlement Settlement, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			settlement = Settlement{}
			err = fmt.Errorf("dispatcher panicked: %v", recovered)
		}
	}()
	return dispatcher.Dispatch(ctx, request, emit)
}
