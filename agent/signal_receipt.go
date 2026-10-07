package agent

import (
	"bytes"
	"encoding/json"
)

// SignalReceipt is an immutable admission and consumption fact from a
// ProcessSnapshot. Consumed payload bytes are released while their normalized
// digest and recipient WaitID remain available for duplicate reconciliation.
// Its Process owner and committer boundary are supplied by that snapshot.
type SignalReceipt struct {
	id              SignalID
	waitID          WaitID
	arrivalSequence uint64
	// A pending receipt keeps the payload; consumption leaves only its digest.
	pendingPayload json.RawMessage
	consumedDigest Digest
	status         SettlementStatus
}

func (s SignalReceipt) ID() SignalID { return s.id }

func (s SignalReceipt) WaitID() (WaitID, bool) { return s.waitID, s.waitID.Valid() }

func (s SignalReceipt) PayloadDigest() Digest {
	if s.pendingPayload != nil {
		return ComputeDigest(s.pendingPayload)
	}
	return s.consumedDigest
}

func (s SignalReceipt) ArrivalSequence() uint64 { return s.arrivalSequence }

// Consumed reports committed consumption, not delivery to a candidate Step.
func (s SignalReceipt) Consumed() bool { return s.pendingPayload == nil }

// PendingSignal returns the retained input only while it remains unconsumed.
func (s SignalReceipt) PendingSignal() (Signal, bool) {
	if s.pendingPayload == nil {
		return Signal{}, false
	}
	return Signal{id: s.id, waitID: s.waitID, payload: bytes.Clone(s.pendingPayload), status: s.status}, true
}

// Matches reports whether this receipt proves admission of the external request.
// Internal wait-opening and child-wait settlement Signals cannot prove external
// admission. A different WaitID or normalized payload remains an identity conflict.
func (s SignalReceipt) Matches(request SignalRequest) bool {
	return !s.id.engineOwned() && request.Valid() && s.id == request.id && s.waitID == request.waitID &&
		s.PayloadDigest() == ComputeDigest(request.payload)
}
