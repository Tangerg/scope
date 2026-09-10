package agent

// SignalReceipt is an immutable admission and consumption fact from a
// ProcessSnapshot. Consumed payload bytes are released while their normalized
// digest and recipient WaitID remain available for duplicate reconciliation.
// Its Process owner and durability boundary are supplied by that snapshot.
type SignalReceipt struct {
	id              SignalID
	waitID          WaitID
	payloadDigest   Digest
	arrivalSequence uint64
	external        bool
	consumed        bool
	pending         Signal
}

func (s SignalReceipt) ID() SignalID { return s.id }

func (s SignalReceipt) WaitID() (WaitID, bool) { return s.waitID, s.waitID.Valid() }

func (s SignalReceipt) PayloadDigest() Digest { return s.payloadDigest }

func (s SignalReceipt) ArrivalSequence() uint64 { return s.arrivalSequence }

// Consumed reports committed consumption, not delivery to a candidate Step.
func (s SignalReceipt) Consumed() bool { return s.consumed }

// PendingSignal returns the retained input only while it remains unconsumed.
func (s SignalReceipt) PendingSignal() (Signal, bool) { return s.pending, s.pending.Valid() }

// Matches reports whether this receipt proves admission of the external request.
// Internal wait-opening and child-wait settlement Signals cannot prove external
// admission. A different WaitID or normalized payload remains an identity conflict.
func (s SignalReceipt) Matches(request SignalRequest) bool {
	return s.external && request.Valid() && s.id == request.id && s.waitID == request.waitID &&
		s.payloadDigest == ComputeDigest(request.payload)
}

func snapshotSignalReceipts(mailbox mailboxWire) []SignalReceipt {
	externalWaits := make(map[WaitID]bool, len(mailbox.Waits))
	for _, wait := range mailbox.Waits {
		externalWaits[wait.WaitID] = wait.ExternallyAddressable
	}
	receipts := make([]SignalReceipt, 0, len(mailbox.Signals))
	for _, record := range mailbox.Signals {
		receipt := SignalReceipt{
			id: record.ID, waitID: snapshotWaitID(record.WaitID), payloadDigest: record.PayloadDigest,
			arrivalSequence: record.ArrivalSequence, consumed: record.ArrivalSequence <= mailbox.SignalCursor,
			external: !record.OpensWait && (record.WaitID == nil || externalWaits[*record.WaitID]),
		}
		if !receipt.consumed {
			receipt.pending = Signal{id: receipt.id, waitID: receipt.waitID, payload: record.Payload}
		}
		receipts = append(receipts, receipt)
	}
	return receipts
}
