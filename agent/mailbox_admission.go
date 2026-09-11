package agent

import "fmt"

// signalAdmission holds only new facts. Historical records remain authoritative
// throughout preflight, and applying an admitted batch cannot fail.
type signalAdmission struct {
	records   []signalRecord
	status    Status
	duplicate bool
}

func newAdmissionRecord(signal Signal, source signalSource) (signalRecord, error) {
	if !signal.Valid() || (source != signalSourceExternal && source != signalSourceChildWait) {
		return signalRecord{}, fmt.Errorf("%w: %w", ErrSignalRejected, ErrInvalidSignal)
	}
	return newSignalRecord(signal, false), nil
}

func (s signalMailbox) prepareAdmission(status Status, currentWaitID WaitID, signals []Signal, source signalSource) (signalAdmission, error) {
	admission := signalAdmission{records: make([]signalRecord, 0, len(signals)), status: status}
	seen := make(map[SignalID]signalRecord, len(signals))
	answered := make(map[WaitID]struct{})
	for _, signal := range signals {
		record, err := newAdmissionRecord(signal, source)
		if err != nil {
			return signalAdmission{}, err
		}
		if previous, found := seen[record.id]; found {
			if !previous.sameContent(record) {
				return signalAdmission{}, ErrSignalConflict
			}
			admission.duplicate = true
			continue
		}
		accepted, err := s.validateRecord(admission.status, record, source)
		if err != nil {
			return signalAdmission{}, err
		}
		if !accepted {
			// A duplicate does not excuse a conflict or unauthorized address later
			// in the batch. Nothing is applied unless every entry passes preflight.
			admission.duplicate = true
			continue
		}
		waitID := record.waitID
		if waitID.Valid() {
			if _, alreadyAnswered := answered[waitID]; alreadyAnswered {
				return signalAdmission{}, ErrSignalRejected
			}
			answered[waitID] = struct{}{}
		}
		if admission.status == StatusWaiting {
			if source == signalSourceExternal {
				wait := s.waits[currentWaitID]
				if waitID != currentWaitID && (waitID.Valid() || wait.externallyAddressable) {
					return signalAdmission{}, ErrSignalRejected
				}
			}
			if waitID == currentWaitID {
				admission.status = StatusRunning
			}
		}
		seen[record.id] = record
		admission.records = append(admission.records, record)
	}
	return admission, nil
}
